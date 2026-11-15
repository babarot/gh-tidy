package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
)

// Changer is what the engine needs to change clones. Each change is made
// in its clone's lane, after the facts it is decided from are read again.
type Changer interface {
	Fetch(ctx context.Context, path string) error
	AdvanceFacts(ctx context.Context, path, name string, source model.DefaultSource) model.AdvanceFacts
	FastForwardRef(ctx context.Context, path, name string) error
	MergeFastForward(ctx context.Context, wt, name string) error
	CleanupFacts(ctx context.Context, c model.Clone) (model.CleanupFacts, error)
	WorktreeDirty(ctx context.Context, wt string) (int, error)
	// RemoveWorktree removes the worktree at wt of the clone at path, as
	// the user's worktree_remove setting says.
	RemoveWorktree(ctx context.Context, path, wt, branch string) error
	BranchExists(ctx context.Context, path, branch string) bool
	DeleteMergedBranch(ctx context.Context, path, branch, base string) error
	DeleteBranch(ctx context.Context, path, branch string) error
	PruneWorktrees(ctx context.Context, path string) error
}

// noChanger refuses every change, for an engine built without one.
type noChanger struct{}

var errNoChanger = errors.New("gh tidy was built without a way to change clones")

func (noChanger) Fetch(context.Context, string) error { return errNoChanger }
func (noChanger) AdvanceFacts(context.Context, string, string, model.DefaultSource) model.AdvanceFacts {
	return model.AdvanceFacts{}
}
func (noChanger) FastForwardRef(context.Context, string, string) error   { return errNoChanger }
func (noChanger) MergeFastForward(context.Context, string, string) error { return errNoChanger }
func (noChanger) CleanupFacts(context.Context, model.Clone) (model.CleanupFacts, error) {
	return model.CleanupFacts{}, errNoChanger
}
func (noChanger) WorktreeDirty(context.Context, string) (int, error)           { return 0, errNoChanger }
func (noChanger) RemoveWorktree(context.Context, string, string, string) error { return errNoChanger }
func (noChanger) BranchExists(context.Context, string, string) bool            { return false }
func (noChanger) DeleteMergedBranch(context.Context, string, string, string) error {
	return errNoChanger
}
func (noChanger) DeleteBranch(context.Context, string, string) error { return errNoChanger }
func (noChanger) PruneWorktrees(context.Context, string) error       { return errNoChanger }

// ErrBusy is returned when a clone has a change in flight: changes to one
// clone are made one at a time.
var ErrBusy = errors.New("busy")

// lane takes key's lane for doing, or returns false when it is taken.
// While it is held, reads of the clone that started before are dropped.
func (e *Engine) lane(key model.CloneKey, doing string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lanes[key] != "" {
		return false
	}
	e.lanes[key] = doing
	e.epoch[key]++
	e.setOp(key, model.Op{Doing: doing})
	for _, o := range e.owners {
		if o.rows[string(key)] != nil {
			o.writes++
		}
	}
	return true
}

// leave gives key's lane back, with what was done.
func (e *Engine) leave(key model.CloneKey, op model.Op) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.lanes, key)
	e.epoch[key]++
	e.setOp(key, op)
	for _, o := range e.owners {
		if o.rows[string(key)] != nil && o.writes > 0 {
			o.writes--
		}
	}
}

// setOp sets the operation shown on every row of key. Called with e.mu
// held.
func (e *Engine) setOp(key model.CloneKey, op model.Op) {
	for _, o := range e.owners {
		if r := o.rows[string(key)]; r != nil {
			r.Op = op
			e.changedOwner(o)
		}
	}
}

// ClearResults puts away the results shown on rows.
func (e *Engine) ClearResults() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, o := range e.owners {
		for _, r := range o.rows {
			if r.Op.Doing == "" && r.Op.Result != "" {
				r.Op = model.Op{}
			}
		}
		e.changedOwner(o)
	}
}

// clone returns the clone of key as last read, from any owner holding it.
func (e *Engine) clone(key model.CloneKey) (model.Clone, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, o := range e.owners {
		if r := o.rows[string(key)]; r != nil && r.Local.Has {
			return r.Local.Value, true
		}
	}
	return model.Clone{}, false
}

// rescan reads the clone again after a change, inside its lane, and puts
// it into every owner holding it or whose directory it sits in: a read of
// it dropped while the change held the lane is made up for here.
func (e *Engine) rescan(_ context.Context, path string) model.Clone {
	c := e.scan(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, o := range e.owners {
		if o.rows[string(c.Key)] != nil || e.inOwnerDir(path, o.login) {
			e.putClone(o, c)
		}
	}
	return c
}

// free returns the keys whose lanes are free, and those taken: a change
// asked for a clone being changed is refused at once rather than queued,
// since what decided it may no longer hold when its turn would come.
func (e *Engine) free(keys []model.CloneKey) (free, busy []model.CloneKey) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, k := range keys {
		if e.lanes[k] != "" {
			busy = append(busy, k)
		} else {
			free = append(free, k)
		}
	}
	return free, busy
}

// Fetch fetches the clones of keys, a few at a time, and returns those it
// refused because a change is in flight in them.
func (e *Engine) Fetch(keys []model.CloneKey) (busy []model.CloneKey) {
	keys, busy = e.free(keys)
	for _, k := range keys {
		e.netPool.add(prioAsked, nil, func() {
			if !e.lane(k, "fetching") {
				return
			}
			c, ok := e.clone(k)
			op := model.Op{Result: "fetched"}
			switch {
			case !ok || !c.HasOrigin():
				op = model.Op{Result: "no origin to fetch", Skipped: true}
			default:
				if err := e.change.Fetch(e.ctx, c.Path); err != nil {
					op = model.Op{Result: lastLine(err), Failed: true}
				}
				e.logChange("fetch", c.Path, op)
				e.rescan(e.ctx, c.Path)
			}
			e.leave(k, op)
			e.readHeavyShown()
		}, nil)
	}
	return busy
}

// Advance fetches each clone of keys and moves its default branch forward
// to origin's, when policy allows it: only fast-forwards, never the branch
// being worked on. What is skipped says why. It returns the clones it
// refused because a change is in flight in them.
func (e *Engine) Advance(keys []model.CloneKey) (busy []model.CloneKey) {
	keys, busy = e.free(keys)
	for _, k := range keys {
		e.netPool.add(prioAsked, nil, func() {
			if !e.lane(k, "fetching") {
				return
			}
			op := e.advance(k)
			e.leave(k, op)
			e.readHeavyShown()
		}, nil)
	}
	return busy
}

func (e *Engine) advance(k model.CloneKey) model.Op {
	ctx := e.ctx
	c, ok := e.clone(k)
	if !ok || !c.HasOrigin() {
		return model.Op{Result: "no origin", Skipped: true}
	}
	if err := e.change.Fetch(ctx, c.Path); err != nil {
		op := model.Op{Result: lastLine(err), Failed: true}
		e.logChange("fetch", c.Path, op)
		return op
	}
	c = e.rescan(ctx, c.Path)
	e.mu.Lock()
	e.setOp(k, model.Op{Doing: "updating " + c.Default})
	e.mu.Unlock()
	d := policy.DecideAdvance(e.change.AdvanceFacts(ctx, c.Path, c.Default, c.DefaultSource))
	var err error
	switch d.Action {
	case policy.AdvanceCurrent:
		return model.Op{Result: c.Default + " up to date", Skipped: true}
	case policy.AdvanceSkip:
		return model.Op{Result: d.Why, Skipped: true}
	case policy.AdvanceRef:
		err = e.change.FastForwardRef(ctx, c.Path, c.Default)
	case policy.AdvanceMerge:
		err = e.change.MergeFastForward(ctx, d.Worktree, c.Default)
	}
	op := model.Op{Result: fmt.Sprintf("%s updated ↓%d", c.Default, d.Commits)}
	if err != nil {
		op = model.Op{Result: lastLine(err), Failed: true}
	}
	e.logChange("advance", c.Path, op)
	e.rescan(ctx, c.Path)
	return op
}

// PlanCleanup reads, now, what d would remove from the clone of key and
// what it keeps, for the user to confirm.
func (e *Engine) PlanCleanup(key model.CloneKey, opts policy.Cleanup, cwd string) (policy.Plan, error) {
	c, ok := e.clone(key)
	if !ok {
		return policy.Plan{}, errors.New("the clone is not read yet")
	}
	f, err := e.change.CleanupFacts(e.ctx, c)
	if err != nil {
		return policy.Plan{}, err
	}
	return policy.DecideCleanup(c, f, opts, cwd), nil
}

// Cleanup carries out confirmed plans, clone by clone. In each clone's
// lane, when its turn comes, the facts are read again and the plan made
// again; only what both plans remove goes, and each worktree's changes are
// read once more right before it is removed. What changed since the
// confirm is left alone, and said so. It returns the clones it refused
// because a change is in flight in them.
func (e *Engine) Cleanup(plans map[model.CloneKey]policy.Plan, opts policy.Cleanup, cwd string) (busy []model.CloneKey) {
	for k, confirmed := range plans {
		if _, b := e.free([]model.CloneKey{k}); len(b) > 0 {
			busy = append(busy, k)
			continue
		}
		e.localPool.add(prioAsked, nil, func() {
			if !e.lane(k, "cleaning up") {
				return
			}
			op := e.cleanup(k, confirmed, opts, cwd)
			e.leave(k, op)
		}, nil)
	}
	return busy
}

func (e *Engine) cleanup(k model.CloneKey, confirmed policy.Plan, opts policy.Cleanup, cwd string) model.Op {
	ctx := e.ctx
	c, ok := e.clone(k)
	if !ok {
		return model.Op{Result: "not read", Skipped: true}
	}
	c = e.rescan(ctx, c.Path)
	f, err := e.change.CleanupFacts(ctx, c)
	if err != nil {
		return model.Op{Result: err.Error(), Failed: true}
	}
	now := policy.DecideCleanup(c, f, opts, cwd)
	items := policy.StillRemoves(confirmed, now)
	left := len(confirmed.Remove) - len(items) // what changed since the confirm
	if opts.Bulk {
		items = policy.WithoutForce(items)
	}
	removed := 0
	var failure string
	pruned := false
	for _, it := range items {
		var err error
		switch it.Kind {
		case policy.ItemPrunable:
			if !pruned {
				err, pruned = e.change.PruneWorktrees(ctx, c.Path), true
			}
		case policy.ItemWorktree:
			n, derr := e.change.WorktreeDirty(ctx, it.Path)
			switch {
			case derr != nil:
				err = derr
			case n > 0:
				left++ // changes appeared right before: kept
				continue
			default:
				err = e.change.RemoveWorktree(ctx, c.Path, it.Path, it.Branch)
				if err == nil && e.change.BranchExists(ctx, c.Path, it.Branch) {
					err = e.change.DeleteMergedBranch(ctx, c.Path, it.Branch, now.Base)
				}
			}
		case policy.ItemBranch:
			if it.Force {
				err = e.change.DeleteBranch(ctx, c.Path, it.Branch)
			} else {
				err = e.change.DeleteMergedBranch(ctx, c.Path, it.Branch, now.Base)
			}
		}
		if err != nil {
			failure = lastLine(err)
			break
		}
		removed++
	}
	op := model.Op{Result: fmt.Sprintf("removed %d", removed)}
	if left > 0 {
		op.Result += fmt.Sprintf(", left %d that changed since", left)
	}
	if failure != "" {
		op = model.Op{Result: fmt.Sprintf("removed %d, then: %s", removed, failure), Failed: true}
	}
	e.logChange("cleanup", c.Path, op)
	e.rescan(ctx, c.Path)
	return op
}

// readHeavyShown reads what a change may have made stale in the owner
// shown: a fetch moves origin/<default>, which GitHub being ahead is
// measured against.
func (e *Engine) readHeavyShown() {
	e.mu.Lock()
	o := e.owners[e.shown]
	e.mu.Unlock()
	if o != nil {
		e.readAhead(o)
	}
}

// logChange logs a change to a clone at info, naming it by its path with
// the home directory as ~.
func (e *Engine) logChange(op, path string, r model.Op) {
	e.log.Info("change", logging.KeyOp, op, logging.KeyRepo, abbrevHome(path), "result", r.Result, "failed", r.Failed, "skipped", r.Skipped)
}

func abbrevHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h) {
		return "~" + p[len(h):]
	}
	return p
}

// lastLine is the last line of err, where git puts what went wrong.
func lastLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
