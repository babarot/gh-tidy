package engine

import (
	"strconv"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/store"
)

// restSeen is the time seen of the repository the cursor rests on, as it
// was when the cursor came.
type restSeen struct {
	k     model.RemoteKey
	since time.Time
	known bool
}

// seenKey is how the seen file names a repository: by GitHub's number for
// it, which a rename or a move to another owner keeps.
func (e *Engine) seenKey(id int64) string { return e.host + "/" + strconv.FormatInt(id, 10) }

// ownerKey is how the seen file names an owner.
func (e *Engine) ownerKey(k model.RemoteKey) string {
	slug := slugOf(k)
	owner, _, _ := strings.Cut(slug, "/")
	return e.host + "/" + strings.ToLower(owner)
}

// since is the time seen of the repository of r: what the user last saw
// of it, or, for one never looked at, when its owner's creation times
// were first read; the later of the two. ok is false when there is
// neither. Called with e.mu held.
func (e *Engine) since(r *model.Row) (time.Time, bool) {
	h := r.Heavy.Value
	if e.rest.k != "" && (r.Remote == e.rest.k || r.Origin == e.rest.k) {
		return e.rest.since, e.rest.known
	}
	base, okBase := e.seenAt.Owners[e.ownerKey(r.Remote)]
	seen, okSeen := e.seenAt.Repos[e.seenKey(h.ID)]
	switch {
	case h.ID == 0 || !okBase && !okSeen:
		return time.Time{}, false
	case seen.After(base):
		return seen, true
	}
	return base, true
}

// newOf counts the open issues and pull requests of r made since the
// repository was seen. A value read without their creation times, or with
// no time seen to tell them by, is not known: never shown as none new.
// Called with e.mu held.
func (e *Engine) newOf(r *model.Row) model.New {
	h := r.Heavy
	if !h.Has || !h.Value.Found || !h.Value.Fields.All(model.NeedNewest) || r.Remote == "" {
		return model.New{}
	}
	since, ok := e.since(r)
	if !ok {
		return model.New{}
	}
	count := func(ts []time.Time) int {
		n := 0
		for _, t := range ts {
			if t.After(since) {
				n++
			}
		}
		return n
	}
	v := h.Value
	n := model.New{Known: true, Since: since, Issues: count(v.IssuesNewest), PRs: count(v.PRsNewest)}
	n.MoreIssues = n.Issues > 0 && n.Issues == len(v.IssuesNewest) && v.Issues > n.Issues
	n.MorePRs = n.PRs > 0 && n.PRs == len(v.PRsNewest) && v.PRs > n.PRs
	return n
}

// setBaselines takes when GitHub answered as the time seen of each owner
// of got that has none: what is made after it is new. Without GitHub's
// time nothing is set, and the next read sets it; the local clock is not
// used, since it may not agree with GitHub's.
func (e *Engine) setBaselines(got []github.HeavyRepo, at time.Time) {
	if at.IsZero() {
		return
	}
	e.mu.Lock()
	e.loadSeen()
	add := store.Seen{Owners: map[string]time.Time{}}
	for _, h := range got {
		if !h.Found {
			continue
		}
		k := e.ownerKey(h.Key)
		if _, ok := e.seenAt.Owners[k]; !ok {
			e.seenAt.Owners[k] = at
			add.Owners[k] = at
		}
	}
	e.mu.Unlock()
	if len(add.Owners) > 0 {
		e.saveSeen(add)
	}
}

// MarkSeen says the user has seen the titles of the repository k listed
// in the pane, the cursor resting on it in the github columns: the time
// seen moves to the newest of them, when the issues and pull requests
// were both read. Its marks stay until the cursor leaves the row.
func (e *Engine) MarkSeen(k model.RemoteKey) {
	e.mu.Lock()
	o := e.owners[e.shown]
	ent := e.ghDetails[k]
	if o == nil || o.login == All || ent == nil || ent.d.State != model.DetailRead {
		e.mu.Unlock()
		return
	}
	d := ent.d.Value
	var r *model.Row
	for _, x := range e.rowsByRemote(o, k) {
		if x.Heavy.Has && x.Heavy.Value.ID != 0 {
			r = x
		}
	}
	newest := d.Newest()
	if r == nil || !d.TitlesAsked || !d.IssuesRead || !d.PRsRead || newest.IsZero() {
		e.mu.Unlock()
		return
	}
	e.loadSeen()
	if e.rest.k != k {
		since, ok := e.since(r)
		e.rest = restSeen{k: k, since: since, known: ok}
	}
	key := e.seenKey(r.Heavy.Value.ID)
	if !newest.After(e.seenAt.Repos[key]) {
		e.mu.Unlock()
		return
	}
	e.seenAt.Repos[key] = newest
	e.mu.Unlock()
	e.saveSeen(store.Seen{Repos: map[string]time.Time{key: newest}})
}

// leaveRest lets go of the repository the cursor rested on, when it moves
// to k: its marks now follow the time seen. Called with e.mu held; it
// reports whether the marks shown change.
func (e *Engine) leaveRest(k model.RemoteKey) bool {
	if e.rest.k == "" || e.rest.k == k {
		return false
	}
	e.rest = restSeen{}
	return true
}

// saveSeen writes what add moved, as the engine's own work, out of the
// screen's way; the file keeps the later time of each key.
func (e *Engine) saveSeen(add store.Seen) {
	if e.store == nil {
		return
	}
	e.localPool.add(prioRest, nil, func() {
		merged, err := e.store.SaveSeen(add)
		if err != nil {
			e.log.Warn("what was seen not kept", "err", err.Error())
			return
		}
		e.mu.Lock()
		for key, t := range merged.Repos {
			if t.After(e.seenAt.Repos[key]) {
				e.seenAt.Repos[key] = t
			}
		}
		for key, t := range merged.Owners {
			if _, ok := e.seenAt.Owners[key]; !ok {
				e.seenAt.Owners[key] = t
			}
		}
		e.mu.Unlock()
	}, func() {})
}
