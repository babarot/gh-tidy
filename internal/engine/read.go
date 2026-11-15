package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
)

// readOwner reads o again: its clones from disk, its list from GitHub
// when it may be read, then the fields the screen needs.
func (e *Engine) readOwner(o *owner) {
	e.mu.Lock()
	gen, login := o.gen, o.login
	e.mu.Unlock()
	alive := e.alive(o, gen)

	e.begin(o, "clones")
	e.localPool.add(prioRest, alive, func() {
		paths, err := e.local.Find(e.ownerDir(login))
		switch {
		case err == nil || os.IsNotExist(err):
			e.forgetMissing(o, paths)
		default:
			// The directory could not be read: what is known of its clones
			// stays rather than turning into repositories not cloned.
			e.log.Warn("could not list clones", logging.KeyOwner, login, "err", err.Error())
		}
		e.scanAll(o, paths, alive, func() {
			e.mu.Lock()
			o.fromCache = false
			e.mu.Unlock()
			e.end(o, "clones")
			e.readHeavy(o)
		})
	}, func() { e.end(o, "clones") })
	e.readList(o, gen, alive)
}

// readAll reads every clone under the root for the All owner, and takes
// GitHub's values as each owner last read them: nothing is asked of
// GitHub.
func (e *Engine) readAll(o *owner) {
	e.mu.Lock()
	gen := o.gen
	e.mu.Unlock()
	alive := e.alive(o, gen)
	e.begin(o, "clones")
	e.localPool.add(prioRest, alive, func() {
		owners, err := e.local.Owners(e.ownerDir(""))
		if err != nil && !os.IsNotExist(err) {
			e.log.Warn("could not list owners", "err", err.Error())
			e.scanAll(o, nil, alive, func() { e.end(o, "clones") })
			return
		}
		var paths []string
		complete := true
		for _, login := range owners {
			ps, err := e.local.Find(e.ownerDir(login))
			if err != nil && !os.IsNotExist(err) {
				complete = false
			}
			paths = append(paths, ps...)
			e.copyGitHubValues(o, login)
		}
		if complete {
			e.forgetMissing(o, paths)
		}
		e.scanAll(o, paths, alive, func() { e.end(o, "clones") })
	}, func() { e.end(o, "clones") })
}

// copyGitHubValues puts into the All owner's rows what owner login's last
// read left: in memory, or else on disk. Rows are copied, never shared.
func (e *Engine) copyGitHubValues(all *owner, login string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var rows []model.Row
	if o := e.owners[strings.ToLower(login)]; o != nil && o != all {
		for _, r := range o.rows {
			rows = append(rows, *r)
		}
	} else if e.store != nil && e.known {
		if c, ok, _ := e.store.LoadOwner(e.host, e.viewer.Login, login); ok {
			rows = c.Rows
		}
	}
	for _, r := range rows {
		if r.Clone == "" {
			continue // All holds clones only
		}
		cp := r
		cp.Op = model.Op{}
		if old := all.rows[r.Key()]; old != nil {
			cp.Local, cp.Op = old.Local, old.Op
		}
		all.rows[r.Key()] = &cp
	}
}

// forgetMissing drops the clones of o no longer on disk; a row that is
// also in the list stays, as not cloned.
func (e *Engine) forgetMissing(o *owner, paths []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	have := map[model.CloneKey]bool{}
	for _, p := range paths {
		have[model.NewCloneKey(p)] = true
	}
	for k, r := range o.rows {
		if r.Clone == "" || have[r.Clone] {
			continue
		}
		delete(o.rows, k)
		if r.Listed.Has && o.login != All && len(e.rowsByRemote(o, r.Remote)) == 0 {
			left := model.Row{Remote: r.Remote, Listed: r.Listed, Heavy: r.Heavy}
			o.rows[left.Key()] = &left
		}
	}
	e.changedOwner(o)
}

// scanAll reads each clone, the rows on the screen first, and calls done
// once every one is back. Reads of the disk are not canceled when the owner
// is left: they end soon, and what they read is the clone's, whoever shows
// it.
func (e *Engine) scanAll(o *owner, paths []string, alive func() bool, done func()) {
	if len(paths) == 0 {
		done()
		return
	}
	left := len(paths)
	for _, p := range paths {
		key := model.NewCloneKey(p)
		e.mu.Lock()
		epoch := e.epoch[key]
		prio := prioRest
		if e.visible[string(key)] {
			prio = prioVisible
		}
		e.mu.Unlock()
		k := e.track.add("clones", 1, "clone", filepath.Base(p), o.login)
		finish := func() {
			k.end("", false) // dropped; a read that ran has ended already
			e.mu.Lock()
			left--
			last := left == 0
			e.mu.Unlock()
			if last {
				done()
			}
		}
		e.localPool.add(prio, alive, func() {
			k.start()
			s := e.scan(p)
			if s.clone.Err != "" {
				k.end("failed", true)
			} else {
				k.end("read", false)
			}
			e.mu.Lock()
			// A change started or ended since the read started: what the
			// change reads afterwards is newer.
			if e.epoch[key] == epoch && e.lanes[key] == nil {
				e.putClone(o, s)
			}
			e.mu.Unlock()
			finish()
		}, finish) // dropped: still counts as back
	}
}

// scan reads the clone at path, measured against GitHub's default branch
// when GitHub was read: origin/HEAD may be stale, or missing.
func (e *Engine) scan(path string) scanned {
	c := e.local.Scan(e.ctx, path)
	if c.Err != "" {
		return scanned{clone: c}
	}
	e.mu.Lock()
	def := ""
	// The owner shown first: All may hold an older copy of the value.
	owners := []*owner{e.owners[e.shown]}
	for _, o := range e.owners {
		owners = append(owners, o)
	}
	for _, o := range owners {
		if o == nil || o.login == All {
			continue
		}
		if r := o.rows[string(c.Key)]; r != nil && r.Heavy.Has && r.Heavy.Value.Default != "" {
			def = r.Heavy.Value.Default
			break
		}
	}
	e.mu.Unlock()
	if def != "" && (def != c.Default || c.DefaultSource != model.DefaultFromGitHub) {
		c = e.local.SetDefault(e.ctx, c, def)
	}
	s := scanned{clone: c}
	if needsFacts(c) {
		s.facts, s.factsErr = e.local.WorktreeFacts(e.ctx, c)
		s.hasFacts = true
	}
	return s
}

// needsFacts reports whether the list needs to know which of c's branches
// are merged: to judge its linked worktrees, or to tell whether a branch
// in no worktree, never pushed or with its upstream deleted, holds work
// found nowhere else.
func needsFacts(c model.Clone) bool {
	if len(c.Linked()) > 0 {
		return true
	}
	held := map[string]bool{}
	for _, w := range c.Worktrees {
		held[w.Holds()] = true
	}
	for _, b := range c.Branches {
		if b.Name != c.Default && !held[b.Name] && (b.Upstream == "" || b.Gone) {
			return true
		}
	}
	return false
}

// scanned is a clone read, with what judging its linked worktrees needs
// when it has any.
type scanned struct {
	clone    model.Clone
	facts    model.CleanupFacts
	factsErr error
	hasFacts bool
}

// putClone puts a clone read into o, joining it with the row of its
// repository from the list. A read that failed keeps what was read before.
// Called with e.mu held.
func (e *Engine) putClone(o *owner, s scanned) {
	now := e.now()
	c := s.clone
	r := o.rows[string(c.Key)]
	if r == nil {
		r = &model.Row{Clone: c.Key}
		o.rows[string(c.Key)] = r
	}
	if c.Err != "" {
		r.Local.Fail(c.Err, now)
		e.changedOwner(o)
		return
	}
	r.Local.Set(c, now)
	e.reads[c.Key]++
	switch {
	case !s.hasFacts:
		r.Facts = model.Read[model.CleanupFacts]{}
	case s.factsErr != nil:
		r.Facts.Fail(s.factsErr.Error(), now)
	default:
		r.Facts.Set(s.facts, now)
	}
	e.forgetDetails(c.Key)
	switch {
	case r.Origin == "" && r.Remote == "":
		r.Origin, r.Remote = c.Remote, c.Remote
	case r.Origin == "":
		r.Origin = c.Remote // a row kept from before origins were kept apart
	case c.Remote != r.Origin:
		// The origin itself changed: what GitHub said of the old one is not
		// this clone's.
		r.Origin, r.Remote = c.Remote, c.Remote
		r.Listed, r.Heavy, r.Ahead = model.Read[model.Remote]{}, model.Read[model.Heavy]{}, model.Read[model.Ahead]{}
	}
	for _, k := range []model.RemoteKey{r.Remote, r.Origin} {
		if k == "" {
			continue
		}
		if lone := o.rows[string(k)]; lone != nil && lone != r && lone.Clone == "" {
			if lone.Listed.Has {
				r.Listed = lone.Listed
			}
			if lone.Heavy.Has && !r.Heavy.Has {
				r.Heavy = lone.Heavy
			}
			delete(o.rows, string(k))
		}
	}
	// Another clone of the same repository may hold what the list and
	// GitHub said of it already.
	for _, s := range e.rowsByRemote(o, r.Remote) {
		if s == r {
			continue
		}
		if !r.Listed.Has && s.Listed.Has {
			r.Listed = s.Listed
		}
		if !r.Heavy.Has && s.Heavy.Has {
			r.Heavy = s.Heavy
		}
	}
	e.changedOwner(o)
}

func (e *Engine) ctxOf(o *owner) context.Context {
	e.mu.Lock()
	defer e.mu.Unlock()
	if o.ctx != nil {
		return o.ctx
	}
	return e.ctx
}

// readList reads o's list when it may be read, is not fresh and is not
// being read: an owner that is neither the viewer nor one of its
// organizations is not listed unasked, only its clones are looked up.
func (e *Engine) readList(o *owner, gen int, alive func() bool) {
	e.mu.Lock()
	kind, login, prev, asked := o.kind, o.login, o.pages, o.asked
	// r asks for the list too, as far as gh tidy reads it at all: not
	// the list of an owner that is not the user's, which only L reads.
	forced := o.refreshList
	o.refreshList = false
	skip := o.listing != 0 || !o.listAt.IsZero() && e.now().Sub(o.listAt) < listFresh && !o.fromCache
	if kind == github.OwnerUser && !asked {
		if o.list == model.ListUnknown {
			o.list, o.listWhy = model.ListPartial, "not your owner: only your clones are looked up"
		}
		skip = true
	}
	if !skip {
		o.listing = gen
	}
	e.mu.Unlock()
	if skip {
		return
	}
	k := e.track.add("GitHub", 1, "github", "list "+login, login)
	done := func() {
		k.end("", false) // dropped; a read that ran has ended already
		e.mu.Lock()
		if o.listing == gen {
			o.listing = 0
		}
		e.mu.Unlock()
		e.end(o, "list")
	}
	prio, pages := prioRest, maxListPages
	if asked {
		prio, pages = prioAsked, askedListPages
	} else if forced {
		prio = prioAsked
	}
	asked = asked || forced
	e.begin(o, "list")
	e.apiPool.add(prio, alive, func() {
		defer done()
		k.start()
		if !asked {
			if ok, why := e.budget.allowAuto("core", false); !ok {
				e.log.Info("list held back", logging.KeyOwner, login, logging.KeyReason, why)
				k.end("held back", false)
				return
			}
		} else if until := e.budget.Paused(); !until.IsZero() {
			e.log.Info("list held back", logging.KeyOwner, login, logging.KeyReason, "waiting out a GitHub rate limit")
			k.end("held back", false)
			return
		}
		l, err := e.gh.List(e.ctxOf(o), login, kind, prev, pages)
		e.budget.saw(l.Rate, err == nil)
		// A page answered 304 costs nothing, and saves a point only when the
		// whole list is unchanged: one changed page has every page read again.
		saved := 0
		if err == nil && l.Unchanged {
			saved = len(l.Pages.ETags)
		}
		e.track.spent(l.Rate.Cost, saved)
		k.end(listNote(l, err))
		if e.takeList(o, gen, login, l, err) {
			e.readHeavy(o) // inside the task, so that Idle waits for what it queues
		}
	}, done)
}

// takeList takes the result of reading o's list, and reports whether
// there is a new list to read the fields of.
func (e *Engine) takeList(o *owner, gen int, login string, l github.Listing, err error) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if o.gen != gen {
		return false // opened again since: a newer reading is on its way
	}
	var rl *github.RateLimitError
	switch {
	case errors.As(err, &rl):
		e.budget.pause(rl)
		return false
	case errors.Is(err, github.ErrNoOwner):
		o.list, o.listWhy = model.ListUnreadable, "no such user or organization on GitHub"
		return false
	case errors.Is(err, context.Canceled):
		return false
	case err != nil:
		e.log.Warn("list failed", logging.KeyOwner, login, "err", err.Error())
		return false
	}
	e.putList(o, l)
	return true
}

// putList puts a list read into o. Called with e.mu held.
func (e *Engine) putList(o *owner, l github.Listing) {
	now := e.now()
	o.pages, o.listAt, o.fromCache = l.Pages, now, false
	o.list, o.listWhy = fromGitHub(l.Completeness), l.Reason
	if l.Unchanged {
		for _, r := range o.rows {
			if r.Listed.Has {
				r.Listed.Tried = now
			}
		}
		e.changedOwner(o)
		return
	}
	listed := map[model.RemoteKey]bool{}
	for _, lr := range l.Repos {
		listed[lr.Key] = true
		rows := e.rowsByRemote(o, lr.Key)
		if len(rows) == 0 {
			r := &model.Row{Remote: lr.Key}
			o.rows[string(lr.Key)] = r
			rows = []*model.Row{r}
		}
		for _, r := range rows {
			r.Listed.Set(model.Remote{Key: lr.Key, Name: lr.Name, Fork: lr.Fork, Archived: lr.Archived, Private: lr.Private,
				PushedAt: lr.PushedAt, DefaultBranch: lr.DefaultBranch, Description: lr.Description}, now)
		}
	}
	// What a complete list no longer holds is gone from GitHub, as far as
	// this token can see: not cloned, it goes; cloned, it is no longer listed.
	if l.Completeness == github.Complete {
		for k, r := range o.rows {
			if r.Remote == "" || listed[r.Remote] || !r.Listed.Has {
				continue
			}
			if r.Clone == "" {
				delete(o.rows, k)
			} else {
				r.Listed = model.Read[model.Remote]{}
			}
		}
	}
	e.changedOwner(o)
}

// readHeavy reads, by name, the fields the screen needs of o's rows that
// lack them or whose values went stale, and are not being read already:
// every clone's default branch, and what the columns shown need, of clones
// and, when they ask, of repositories not cloned. The rows on the screen go
// first. Several clones of one repository are read once.
func (e *Engine) readHeavy(o *owner) {
	defer e.busy()()
	e.mu.Lock()
	if o.login == All || e.owners[e.shown] != o {
		e.mu.Unlock()
		return
	}
	gen, needs, now := o.gen, e.needs, e.now()
	// r asked for everything again: every field is stale, and the reads
	// are asked for, not held back by the budget gh tidy keeps for itself.
	force := o.refresh
	o.refresh = false
	want := github.Want{Default: true, CI: needs.Has(model.NeedCI), Counts: needs.Has(model.NeedCounts), Alerts: needs.Has(model.NeedAlerts)}
	fields := fieldsOf(want)
	extra := fields != 0
	seen := map[string]bool{}
	var slugs []string
	visible := map[string]bool{}
	for _, r := range o.rows {
		if r.Remote == "" || !strings.HasPrefix(string(r.Remote), e.host+"/") {
			continue // no origin, or one on another host than the one read
		}
		if r.Clone == "" && (!needs.Has(model.NeedNotCloned) || !r.Listed.Has || r.Listed.Value.Fork || r.Listed.Value.Archived) {
			continue
		}
		if m, ok := o.inflight[r.Remote]; ok && m.fields&fields == fields {
			continue // being read already, with what is needed
		}
		h := r.Heavy
		var stale bool
		switch {
		case force:
			stale = true
		case h.Last == model.Denied:
			// GitHub would not give it: asked again only after a while, so
			// that what a token cannot see is not asked for on every tick.
			stale = now.Sub(h.Tried) > unlistedOids
		case h.Last == model.Failed:
			stale = now.Sub(h.Tried) > retryFailed
		case !h.Has:
			stale = true
		case r.Clone != "" && r.Listed.Has && !r.Listed.Value.PushedAt.Equal(h.Value.PushedAt):
			stale = true // pushed since it was read
		case r.Clone != "" && r.Listed.Has && h.Value.Default != "" && r.Listed.Value.DefaultBranch != h.Value.Default:
			stale = true // the default branch changed; an empty repository has none to compare
		case r.Clone != "" && !r.Listed.Has && h.OlderThan(unlistedOids, now):
			stale = true // no list says when it changed: read again by time
		case extra && (h.OlderThan(countsFresh, now) || h.Value.Fields&fields != fields):
			stale = true // the fields the screen needs are old, or were never asked for
		}
		slug := slugOf(r.Remote)
		if !stale || seen[slug] {
			continue
		}
		seen[slug] = true
		slugs = append(slugs, slug)
		o.inflight[r.Remote] = mark{fields: fields, gen: gen}
		if e.visible[r.Key()] {
			visible[slug] = true
		}
	}
	e.mu.Unlock()
	slices.SortStableFunc(slugs, func(a, b string) int {
		if visible[a] != visible[b] {
			if visible[a] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	alive := e.alive(o, gen)
	size := e.budget.batchSize(heavyBatch)
	for i := 0; i < len(slugs); i += size {
		part := slugs[i:min(len(slugs), i+size)]
		prio := prioRest
		if visible[part[0]] {
			prio = prioVisible
		}
		if force {
			prio = prioAsked
		}
		e.queueHeavy(o, gen, part, want, prio, alive, true, force)
	}
	if len(slugs) == 0 {
		e.readAhead(o)
	}
}

// queueHeavy reads one batch. A batch GitHub gave up on is split in two and
// read once more, smaller.
func (e *Engine) queueHeavy(o *owner, gen int, part []string, want github.Want, prio int, alive func() bool, retry, asked bool) {
	release := func() { e.release(o, part, gen) }
	k := e.track.add("GitHub", len(part), "github", fmt.Sprintf("fields · %d repositories", len(part)), o.login)
	e.begin(o, "GitHub")
	e.apiPool.add(prio, alive, func() {
		defer e.end(o, "GitHub")
		k.start()
		held := false
		if asked {
			held = !e.budget.Paused().IsZero()
		} else if ok, why := e.budget.allowAuto("graphql", true); !ok {
			e.log.Info("reads held back", logging.KeyOwner, o.login, logging.KeyReason, why, "repos", len(part))
			held = true
		}
		if held {
			k.end("held back", false)
			release()
			return
		}
		got, rate, err := e.gh.Heavy(e.ctxOf(o), part, want)
		e.budget.saw(rate, err == nil)
		e.track.spent(rate.Cost, 0)
		k.end(heavyNote(rate, err))
		var rl *github.RateLimitError
		switch {
		case errors.Is(err, github.ErrTimeout):
			e.budget.shrink()
			if retry && len(part) > 1 {
				h := len(part) / 2
				e.queueHeavy(o, gen, part[:h], want, prio, alive, false, asked)
				e.queueHeavy(o, gen, part[h:], want, prio, alive, false, asked)
				return
			}
			e.failHeavy(o, gen, part, "GitHub took too long")
			return
		case errors.As(err, &rl):
			e.budget.pause(rl)
			e.failHeavy(o, gen, part, rl.Error())
			return
		case errors.Is(err, context.Canceled):
			release() // the owner was left: nothing went wrong with the values
			return
		case err != nil:
			e.failHeavy(o, gen, part, err.Error())
			return
		}
		if again := e.putHeavy(o, gen, got, fieldsOf(want)); len(again) > 0 {
			// GitHub named another default branch than the clone was
			// measured on: measure those clones again now.
			e.scanAll(o, again, alive, func() { e.readAhead(o) })
			return
		}
		e.readAhead(o)
	}, func() { k.end("", false); release(); e.end(o, "GitHub") })
}

// listNote and heavyNote say how a read went, without an error's text.
func listNote(l github.Listing, err error) (string, bool) {
	var rl *github.RateLimitError
	switch {
	case errors.As(err, &rl):
		return "rate limited", true
	case errors.Is(err, context.Canceled):
		return "", false
	case err != nil:
		return "failed", true
	case l.Unchanged:
		return "304 not modified", false
	}
	return fmt.Sprintf("%d repositories", len(l.Repos)), false
}

func heavyNote(rate github.Rate, err error) (string, bool) {
	var rl *github.RateLimitError
	switch {
	case errors.Is(err, github.ErrTimeout):
		return "GitHub gave up", true
	case errors.As(err, &rl):
		return "rate limited", true
	case errors.Is(err, context.Canceled):
		return "", false
	case err != nil:
		return "failed", true
	}
	return fmt.Sprintf("cost %d", rate.Cost), false
}

// release forgets that the repositories of part are being read by
// generation gen; a newer generation's marks stay.
func (e *Engine) release(o *owner, part []string, gen int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range part {
		k := model.NewRemoteKey(e.host, s)
		if m, ok := o.inflight[k]; ok && m.gen == gen {
			delete(o.inflight, k)
		}
	}
}

func (e *Engine) failHeavy(o *owner, gen int, part []string, why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for _, s := range part {
		k := model.NewRemoteKey(e.host, s)
		if m, ok := o.inflight[k]; ok && m.gen == gen {
			delete(o.inflight, k)
		}
		for _, r := range e.rowsByRemote(o, k) {
			r.Heavy.Fail(why, now)
		}
	}
	e.changedOwner(o)
}

// putHeavy puts what GitHub said of repositories asked for by name into
// every row of each. A repository renamed since takes its new name; the
// row keeps its origin apart, so a clone read again does not undo it. It
// returns the clones measured on another default branch than GitHub's.
func (e *Engine) putHeavy(o *owner, gen int, got []github.HeavyRepo, fields model.Need) (again []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for _, h := range got {
		if m, ok := o.inflight[h.Asked]; ok && m.gen == gen {
			delete(o.inflight, h.Asked)
		}
		for _, r := range e.rowsByRemote(o, h.Asked) {
			switch {
			case h.Err != "":
				r.Heavy.Fail(h.Err, now)
			case !h.Found:
				r.Heavy.Deny("not visible to this token: deleted, moved, or no access", now)
			default:
				if h.Key != h.Asked && r.Clone != "" {
					if lone := o.rows[string(h.Key)]; lone != nil && lone.Clone == "" {
						r.Listed = lone.Listed
						delete(o.rows, string(h.Key))
					}
					r.Remote = h.Key
				}
				r.Heavy.Set(model.Heavy{Key: h.Key, Found: true, PushedAt: h.PushedAt, Default: h.Default, DefaultOid: h.DefaultOid, CI: h.CI,
					Issues: h.Issues, PRs: h.PRs, Alerts: h.Alerts, AlertsOff: h.AlertsOff, AlertsDenied: h.AlertsDenied, Fields: fields}, now)
				if c := r.Local.Value; r.Local.Has && h.Default != "" && (c.Default != h.Default || c.DefaultSource != model.DefaultFromGitHub) {
					again = append(again, c.Path)
				}
			}
		}
	}
	e.changedOwner(o)
	return again
}

// fieldsOf is what a read asked for, as the screen names its needs.
func fieldsOf(w github.Want) model.Need {
	var n model.Need
	if w.CI {
		n |= model.NeedCI
	}
	if w.Counts {
		n |= model.NeedCounts
	}
	if w.Alerts {
		n |= model.NeedAlerts
	}
	return n
}

// rowsByRemote finds o's rows of a repository: its clones, or the row of
// the repository not cloned. Called with e.mu held.
func (e *Engine) rowsByRemote(o *owner, k model.RemoteKey) []*model.Row {
	if k == "" {
		return nil
	}
	var out []*model.Row
	for _, r := range o.rows {
		if r.Remote == k || r.Origin == k {
			out = append(out, r)
		}
	}
	return out
}

// readAhead decides, for each clone whose pair of commits (GitHub's, and
// its origin/<default>) changed since, whether GitHub is ahead of it,
// measured on GitHub's default branch.
func (e *Engine) readAhead(o *owner) {
	e.mu.Lock()
	gen := o.gen
	type job struct {
		key        string
		path, name string
		oid        string
	}
	var jobs []job
	for k, r := range o.rows {
		if !r.Local.Has || !r.Heavy.Has || r.Heavy.Value.DefaultOid == "" || r.Heavy.Value.Default == "" {
			continue
		}
		c, h := r.Local.Value, r.Heavy.Value
		if r.Ahead.Has && r.Ahead.Value.Remote == h.DefaultOid && r.Ahead.Value.Local == c.DefaultOid {
			continue
		}
		jobs = append(jobs, job{key: k, path: c.Path, name: h.Default, oid: h.DefaultOid})
	}
	e.mu.Unlock()
	alive := e.alive(o, gen)
	for _, j := range jobs {
		e.localPool.add(prioRest, alive, func() {
			a := e.local.RemoteAhead(e.ctx, j.path, j.name, j.oid)
			e.mu.Lock()
			if r := o.rows[j.key]; r != nil {
				r.Ahead.Set(a, e.now())
				e.changedOwner(o)
			}
			e.mu.Unlock()
		}, nil)
	}
}

// Tick reads again what went stale in the owner shown, and what was held
// back by the budget: the screen calls it on its clock, since the engine
// keeps none.
func (e *Engine) Tick() {
	defer e.busy()()
	e.mu.Lock()
	o := e.owners[e.shown]
	if o == nil || o.login == All {
		e.mu.Unlock()
		return
	}
	gen := o.gen
	e.mu.Unlock()
	e.readList(o, gen, e.alive(o, gen))
	e.readHeavy(o)
}

// inOwnerDir reports whether the clone at path sits in login's directory.
func (e *Engine) inOwnerDir(path, login string) bool {
	return login == All || filepath.Dir(path) == e.ownerDir(login)
}

// slugOf is owner/name of a remote key.
func slugOf(k model.RemoteKey) string {
	_, slug, _ := strings.Cut(string(k), "/")
	return slug
}
