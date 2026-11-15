package engine

import (
	"context"
	"errors"
	"os"
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
		if err != nil && !os.IsNotExist(err) {
			e.log.Warn("could not list clones", logging.KeyOwner, login, "err", err.Error())
		}
		e.forgetMissing(o, paths)
		e.scanAll(o, paths, alive, func() {
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
		owners, _ := e.local.Owners(e.ownerDir(""))
		var paths []string
		for _, login := range owners {
			ps, _ := e.local.Find(e.ownerDir(login))
			paths = append(paths, ps...)
			e.copyGitHubValues(o, login)
		}
		e.forgetMissing(o, paths)
		e.scanAll(o, paths, alive, func() { e.end(o, "clones") })
	}, func() { e.end(o, "clones") })
}

// copyGitHubValues puts into the All owner's rows what owner login's last
// read left: in memory, or else on disk.
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
		if old := all.rows[r.Key()]; old != nil && old.Local.Has {
			cp.Local = old.Local
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
		if r.Listed.Has && o.login != All {
			left := model.Row{Remote: r.Remote, Listed: r.Listed, Heavy: r.Heavy}
			o.rows[left.Key()] = &left
		}
	}
	e.changedOwner(o)
}

// scanAll reads each clone, the rows on the screen first, and calls done
// once every one is back.
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
		finish := func() {
			e.mu.Lock()
			left--
			last := left == 0
			e.mu.Unlock()
			if last {
				done()
			}
		}
		e.localPool.add(prio, alive, func() {
			c := e.local.Scan(e.ctxOf(o), p)
			e.mu.Lock()
			// A change made since the read started makes it older than what
			// the change read afterwards.
			if e.epoch[key] == epoch && e.lanes[key] == "" {
				e.putClone(o, c)
			}
			e.mu.Unlock()
			finish()
		}, finish) // dropped: still counts as back
	}
}

// putClone puts a clone read into o, joining it with the row of its
// repository from the list. Called with e.mu held.
func (e *Engine) putClone(o *owner, c model.Clone) {
	r := o.rows[string(c.Key)]
	if r == nil {
		r = &model.Row{Clone: c.Key}
		o.rows[string(c.Key)] = r
	}
	r.Local.Set(c, e.now())
	if c.Remote != r.Remote {
		r.Remote = c.Remote
	}
	if r.Remote != "" {
		if lone := o.rows[string(r.Remote)]; lone != nil && lone != r && lone.Clone == "" {
			r.Listed, r.Heavy = lone.Listed, lone.Heavy
			delete(o.rows, string(r.Remote))
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

// readList reads o's list when it may be read and is not fresh: an owner
// that is neither the viewer nor one of its organizations is not listed
// unasked, only its clones are looked up.
func (e *Engine) readList(o *owner, gen int, alive func() bool) {
	e.mu.Lock()
	kind, login, prev := o.kind, o.login, o.pages
	fresh := !o.listAt.IsZero() && e.now().Sub(o.listAt) < listFresh && !o.fromCache
	if kind == github.OwnerUser {
		if o.list == model.ListUnknown {
			o.list, o.listWhy = model.ListPartial, "not your owner: only your clones are looked up"
		}
		fresh = true
	}
	e.mu.Unlock()
	if fresh {
		return
	}
	e.begin(o, "list")
	e.apiPool.add(prioRest, alive, func() {
		defer e.end(o, "list")
		if ok, why := e.budget.allowAuto(false); !ok {
			e.log.Info("list held back", logging.KeyOwner, login, logging.KeyReason, why)
			return
		}
		l, err := e.gh.List(e.ctxOf(o), login, kind, prev, maxListPages)
		e.budget.saw(l.Rate)
		if e.takeList(o, gen, login, l, err) {
			e.readHeavy(o) // inside the task, so that Idle waits for what it queues
		}
	}, func() { e.end(o, "list") })
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
		return
	}
	listed := map[model.RemoteKey]bool{}
	byRemote := map[model.RemoteKey]*model.Row{}
	for _, r := range o.rows {
		if r.Remote != "" {
			byRemote[r.Remote] = r
		}
	}
	for _, lr := range l.Repos {
		listed[lr.Key] = true
		r := byRemote[lr.Key]
		if r == nil {
			r = &model.Row{Remote: lr.Key}
			o.rows[string(lr.Key)] = r
		}
		r.Listed.Set(model.Remote{Key: lr.Key, Name: lr.Name, Fork: lr.Fork, Archived: lr.Archived, Private: lr.Private,
			PushedAt: lr.PushedAt, DefaultBranch: lr.DefaultBranch, Description: lr.Description}, now)
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
// lack them or whose values went stale: every clone's default branch, and
// what the columns shown need, of clones and, when they ask, of
// repositories not cloned. The rows on the screen go first.
func (e *Engine) readHeavy(o *owner) {
	e.mu.Lock()
	if o.login == All || e.owners[e.shown] != o {
		e.mu.Unlock()
		return
	}
	gen, needs, now := o.gen, e.needs, e.now()
	want := github.Want{Default: true, CI: needs.Has(model.NeedCI), Counts: needs.Has(model.NeedCounts), Alerts: needs.Has(model.NeedAlerts)}
	extra := want.CI || want.Counts || want.Alerts
	var slugs []string
	visible := map[string]bool{}
	for _, r := range o.rows {
		if r.Remote == "" || r.Local.Has && r.Local.Value.Remote == "" {
			continue
		}
		if r.Clone == "" && (!needs.Has(model.NeedNotCloned) || !r.Listed.Has || r.Listed.Value.Fork || r.Listed.Value.Archived) {
			continue
		}
		h := r.Heavy
		stale := !h.Has || h.Last == model.Failed
		if r.Clone != "" && h.Has {
			switch {
			case r.Listed.Has && (!r.Listed.Value.PushedAt.Equal(h.Value.PushedAt) || r.Listed.Value.DefaultBranch != h.Value.Default):
				stale = true // pushed, or the default branch changed, since it was read
			case !r.Listed.Has && h.OlderThan(unlistedOids, now):
				stale = true // no list says when it changed: read again by time
			}
		}
		fields := needs &^ model.NeedNotCloned
		if extra && (h.OlderThan(countsFresh, now) || h.Value.Fields&fields != fields) {
			stale = true // the fields the screen needs are old, or were never asked for
		}
		if h.Last == model.Denied && !extra && h.Has {
			stale = false
		}
		if stale {
			slug := slugOf(r.Remote)
			slugs = append(slugs, slug)
			if e.visible[r.Key()] {
				visible[slug] = true
			}
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
		e.queueHeavy(o, gen, part, want, prio, alive, true)
	}
	if len(slugs) == 0 {
		e.readAhead(o)
	}
}

// queueHeavy reads one batch. A batch GitHub gave up on is split in two and
// read once more, smaller.
func (e *Engine) queueHeavy(o *owner, gen int, part []string, want github.Want, prio int, alive func() bool, retry bool) {
	e.begin(o, "GitHub")
	e.apiPool.add(prio, alive, func() {
		defer e.end(o, "GitHub")
		if ok, why := e.budget.allowAuto(true); !ok {
			e.log.Info("reads held back", logging.KeyOwner, o.login, logging.KeyReason, why, "repos", len(part))
			return
		}
		got, rate, err := e.gh.Heavy(e.ctxOf(o), part, want)
		e.budget.saw(rate)
		var rl *github.RateLimitError
		switch {
		case errors.Is(err, github.ErrTimeout):
			e.budget.shrink()
			if retry && len(part) > 1 {
				h := len(part) / 2
				e.queueHeavy(o, gen, part[:h], want, prio, alive, false)
				e.queueHeavy(o, gen, part[h:], want, prio, alive, false)
				return
			}
			e.failHeavy(o, part, "GitHub took too long")
			return
		case errors.As(err, &rl):
			e.budget.pause(rl)
			e.failHeavy(o, part, rl.Error())
			return
		case err != nil:
			e.failHeavy(o, part, err.Error())
			return
		}
		e.putHeavy(o, got, fieldsOf(want))
		e.readAhead(o)
	}, func() { e.end(o, "GitHub") })
}

func (e *Engine) failHeavy(o *owner, part []string, why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for _, s := range part {
		if r := e.rowByRemote(o, model.NewRemoteKey(e.host, s)); r != nil {
			r.Heavy.Fail(why, now)
		}
	}
	e.changedOwner(o)
}

// putHeavy puts what GitHub said of repositories asked for by name into o.
// A repository renamed since takes its new name.
func (e *Engine) putHeavy(o *owner, got []github.HeavyRepo, fields model.Need) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	for _, h := range got {
		r := e.rowByRemote(o, h.Asked)
		if r == nil {
			continue
		}
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
		}
	}
	e.changedOwner(o)
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

// rowByRemote finds o's row of a repository. Called with e.mu held.
func (e *Engine) rowByRemote(o *owner, k model.RemoteKey) *model.Row {
	for _, r := range o.rows {
		if r.Remote == k {
			return r
		}
	}
	return nil
}

// readAhead decides, for each clone whose pair of commits (GitHub's, and
// its origin/<default>) changed since, whether GitHub is ahead of it.
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
		if !r.Local.Has || !r.Heavy.Has || r.Heavy.Value.DefaultOid == "" || r.Local.Value.Default == "" {
			continue
		}
		c, h := r.Local.Value, r.Heavy.Value
		if r.Ahead.Has && r.Ahead.Value.Remote == h.DefaultOid && r.Ahead.Value.Local == c.DefaultOid {
			continue
		}
		jobs = append(jobs, job{key: k, path: c.Path, name: c.Default, oid: h.DefaultOid})
	}
	e.mu.Unlock()
	alive := e.alive(o, gen)
	for _, j := range jobs {
		e.localPool.add(prioRest, alive, func() {
			a := e.local.RemoteAhead(e.ctxOf(o), j.path, j.name, j.oid)
			e.mu.Lock()
			if r := o.rows[j.key]; r != nil {
				r.Ahead.Set(a, e.now())
				e.changedOwner(o)
			}
			e.mu.Unlock()
		}, nil)
	}
}

// slugOf is owner/name of a remote key.
func slugOf(k model.RemoteKey) string {
	_, slug, _ := strings.Cut(string(k), "/")
	return slug
}
