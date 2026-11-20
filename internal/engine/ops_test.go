package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
)

// fakeChanger records the changes asked for and answers facts as told.
type fakeChanger struct {
	mu       sync.Mutex
	advance  model.AdvanceFacts
	cleanup  model.CleanupFacts
	dirty    map[string]int // worktree path to changes, read right before removing
	calls    []string
	block    chan struct{} // when set, Fetch waits for it
	fetching chan struct{} // when set, closed once Fetch starts
}

func (f *fakeChanger) record(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakeChanger) Fetch(ctx context.Context, path string) error {
	f.mu.Lock()
	block, started := f.block, f.fetching
	f.fetching = nil
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if block != nil {
		<-block
	}
	f.record("fetch " + path)
	return nil
}
func (f *fakeChanger) AdvanceFacts(_ context.Context, path, name string, src model.DefaultSource) model.AdvanceFacts {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.advance
	a.Default, a.DefaultSource = name, src
	return a
}
func (f *fakeChanger) FastForwardRef(_ context.Context, path, name string) error {
	f.record("ff-ref " + name)
	return nil
}
func (f *fakeChanger) MergeFastForward(_ context.Context, wt, name string) error {
	f.record("ff-merge " + wt)
	return nil
}
func (f *fakeChanger) CleanupFacts(context.Context, model.Clone) (model.CleanupFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cleanup, nil
}
func (f *fakeChanger) WorktreeDirty(_ context.Context, wt string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dirty[wt], nil
}
func (f *fakeChanger) RemoveWorktree(_ context.Context, path, wt, branch string) error {
	f.record("remove " + wt)
	return nil
}
func (f *fakeChanger) BranchExists(context.Context, string, string) bool { return true }
func (f *fakeChanger) DeleteMergedBranch(_ context.Context, path, branch, base string) error {
	f.record("delete " + branch)
	return nil
}
func (f *fakeChanger) DeleteBranch(_ context.Context, path, branch string) error {
	f.record("force-delete " + branch)
	return nil
}
func (f *fakeChanger) PruneWorktrees(context.Context, string) error { f.record("prune"); return nil }

func opsWorld(t *testing.T) (*world, *fakeChanger, *Engine) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app")
	ch := &fakeChanger{dirty: map[string]int{}}
	e := New(Options{Roots: []string{root}, Local: w.local, Change: ch, GitHub: w.gh, Store: w.store, Now: w.clock.now})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Open("me")
	idle(t, e)
	return w, ch, e
}

const app = model.CloneKey("/src/github.com/me/app")

func TestAdvanceMovesTheDefaultBranch(t *testing.T) {
	_, ch, e := opsWorld(t)
	ch.advance = model.AdvanceFacts{LocalExists: true, RemoteExists: true, Upstream: "origin/main", Behind: 3, FastForward: true, Holders: 0}
	e.Advance([]model.CloneKey{app})
	idle(t, e)
	if !slices.Contains(ch.calls, "ff-ref main") {
		t.Errorf("calls = %v", ch.calls)
	}
	if r := rowOf(e.Owner(), string(app)); r.Op.Result != "main updated ↓3" || r.Op.Failed || r.Op.Doing != "" {
		t.Errorf("op = %+v", r.Op)
	}
	e.ClearResults()
	if r := rowOf(e.Owner(), string(app)); r.Op != (model.Op{}) {
		t.Errorf("results not put away: %+v", r.Op)
	}
}

func TestAdvanceSaysWhyItSkips(t *testing.T) {
	_, ch, e := opsWorld(t)
	ch.advance = model.AdvanceFacts{LocalExists: true, RemoteExists: true, Upstream: "origin/main", Behind: 3, FastForward: true, CheckedOut: "/src/github.com/me/app", Holders: 1, Dirty: 2}
	e.Advance([]model.CloneKey{app})
	idle(t, e)
	for _, c := range ch.calls {
		if strings.HasPrefix(c, "ff-") {
			t.Errorf("moved under uncommitted changes: %v", ch.calls)
		}
	}
	if r := rowOf(e.Owner(), string(app)); !r.Op.Skipped || !strings.Contains(r.Op.Result, "uncommitted") {
		t.Errorf("op = %+v", r.Op)
	}
}

func TestOneChangeAtATimeInAClone(t *testing.T) {
	_, ch, e := opsWorld(t)
	ch.block, ch.fetching = make(chan struct{}), make(chan struct{})
	started := ch.fetching
	e.Fetch([]model.CloneKey{app})
	<-started
	if e.lane(app, "another") {
		t.Error("a second change took the lane of a clone being fetched")
	}
	close(ch.block)
	idle(t, e)
	if !e.lane(app, "after") {
		t.Error("the lane was not given back")
	}
}

func TestAReadStartedBeforeAChangeIsDropped(t *testing.T) {
	w, _, e := opsWorld(t)
	// A read of the clone starts, and waits.
	hold, held := make(chan struct{}), make(chan struct{})
	w.local.mu.Lock()
	w.local.hold, w.local.held = map[string]chan struct{}{string(app): hold}, held
	w.local.mu.Unlock()
	w.local.add("me", "app", "me/app", func(c *model.Clone) { c.Head = "before-the-change" })
	e.Open("me")
	<-held // the read has started
	// A change starts and ends meanwhile, and reads the clone afterwards.
	if !e.lane(app, "pulling") {
		t.Fatal("no lane")
	}
	w.local.add("me", "app", "me/app", func(c *model.Clone) { c.Head = "after-the-change" })
	w.local.mu.Lock()
	w.local.hold = nil
	w.local.mu.Unlock()
	e.leave(app, model.Op{Result: "pulled"})
	e.rescan(context.Background(), string(app))
	// The read begun before ends last, with what it saw then.
	w.local.add("me", "app", "me/app", func(c *model.Clone) { c.Head = "before-the-change" })
	close(hold)
	idle(t, e)
	if got := rowOf(e.Owner(), string(app)).Local.Value.Head; got != "after-the-change" {
		t.Errorf("head = %q: a read begun before the change put back an older state", got)
	}
}

func TestAChangeToABusyCloneIsRefused(t *testing.T) {
	_, _, e := opsWorld(t)
	if !e.lane(app, "pulling") {
		t.Fatal("no lane")
	}
	if busy := e.Advance([]model.CloneKey{app}); len(busy) != 1 {
		t.Errorf("busy = %v", busy)
	}
	if busy := e.Cleanup(map[model.CloneKey]policy.Plan{app: {}}, policy.Cleanup{}, ""); len(busy) != 1 {
		t.Errorf("busy = %v", busy)
	}
}

func TestCleanupLeavesWhatChangedSinceTheConfirm(t *testing.T) {
	w, ch, e := opsWorld(t)
	w.local.add("me", "app", "me/app", func(c *model.Clone) {
		c.Worktrees = []model.Worktree{{Path: "/src/github.com/me/app", Branch: "main", Main: true},
			{Path: "/wt/done", Branch: "done"}, {Path: "/wt/busy-now", Branch: "busy-now"}}
		c.Branches = []model.Branch{{Name: "main"}, {Name: "done"}, {Name: "busy-now"}, {Name: "squashed", Upstream: "origin/squashed", Gone: true}}
	})
	e.Open("me")
	idle(t, e)
	ch.cleanup = model.CleanupFacts{Base: "origin/main", Merged: map[string]bool{"main": true, "done": true, "busy-now": true},
		WorkedOn: map[string]bool{"done": true, "busy-now": true, "squashed": true}}
	opts := policy.Cleanup{DeleteGone: true}
	plan, err := e.PlanCleanup(app, opts, "")
	if err != nil || len(plan.Remove) != 3 {
		t.Fatalf("plan: %+v, %v", plan.Remove, err)
	}
	// After the confirm, an agent starts working in busy-now, and done is
	// no longer merged (its branch moved): the plan made again leaves it.
	ch.dirty["/wt/busy-now"] = 2
	ch.mu.Lock()
	ch.cleanup.Merged = map[string]bool{"main": true, "busy-now": true}
	ch.mu.Unlock()
	e.Cleanup(map[model.CloneKey]policy.Plan{app: plan}, opts, "")
	idle(t, e)
	if slices.Contains(ch.calls, "remove /wt/busy-now") {
		t.Errorf("a worktree with changes since the confirm was removed: %v", ch.calls)
	}
	if slices.Contains(ch.calls, "remove /wt/done") {
		t.Errorf("a worktree no longer merged when its turn came was removed: %v", ch.calls)
	}
	if !slices.Contains(ch.calls, "force-delete squashed") {
		t.Errorf("calls = %v", ch.calls)
	}
	if r := rowOf(e.Owner(), string(app)); !strings.Contains(r.Op.Result, "left 2") {
		t.Errorf("op = %+v", r.Op)
	}
	ch.mu.Lock()
	ch.cleanup.Merged = map[string]bool{"main": true, "done": true, "busy-now": true}
	ch.mu.Unlock()

	// D across clones never forces a branch.
	ch.calls = nil
	bulk := policy.Cleanup{DeleteGone: true, Bulk: true}
	e.Cleanup(map[model.CloneKey]policy.Plan{app: plan}, bulk, "")
	idle(t, e)
	if slices.Contains(ch.calls, "force-delete squashed") {
		t.Errorf("D forced a branch: %v", ch.calls)
	}
}

type fakeCloner struct {
	mu    sync.Mutex
	local *fakeLocal
	err   error
	asked []string
}

func (f *fakeCloner) Clone(_ context.Context, host, slug, dir string) error {
	f.mu.Lock()
	f.asked = append(f.asked, host+" "+slug+" "+dir)
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	owner, name, _ := strings.Cut(slug, "/")
	f.local.add(owner, name, slug, nil)
	return nil
}

func TestCloneMakesTheRowAClone(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool")
	cl := &fakeCloner{local: w.local}
	e := New(Options{Roots: []string{root}, Local: w.local, GitHub: w.gh, Store: w.store, Cloner: cl, Now: w.clock.now})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.Open("me")
	idle(t, e)
	dir, err := e.Clone("github.com/me/tool")
	if err != nil || dir != "/src/github.com/me/tool" {
		t.Fatalf("Clone = %q, %v", dir, err)
	}
	idle(t, e)
	if r := rowOf(e.Owner(), "/src/github.com/me/tool"); r == nil || r.Op.Result != "cloned" || !r.Listed.Has {
		t.Fatalf("row after clone = %+v", r)
	}
	if r := rowOf(e.Owner(), "github.com/me/tool"); r != nil {
		t.Error("the row of the repository not cloned stayed")
	}
	if _, err := e.Clone("github.com/me/app"); err == nil {
		t.Error("cloned a row that is a clone")
	}
	cl.err = errors.New("fatal: could not\nRepository not found")
	w.gh.setList("me", github.Complete, "e2", "app", "tool", "gone")
	e.ReadList()
	idle(t, e)
	if _, err := e.Clone("github.com/me/gone"); err != nil {
		t.Fatal(err)
	}
	idle(t, e)
	if r := rowOf(e.Owner(), "github.com/me/gone"); r == nil || !r.Op.Failed || r.Op.Result != "Repository not found" {
		t.Errorf("failed clone row = %+v", r)
	}
}

func TestReadListReadsAnOwnerNotYours(t *testing.T) {
	w := newWorld(t)
	w.local.add("bigorg", "tool", "bigorg/tool", nil)
	w.gh.setList("bigorg", github.Complete, "b1", "tool", "other")
	e := w.engine()
	e.Open("bigorg")
	idle(t, e)
	e.ReadList()
	idle(t, e)
	if !slices.Contains(w.gh.listed, "bigorg") {
		t.Fatal("L did not read the list")
	}
	if o := e.Owner(); o.List != model.ListComplete || rowOf(o, "github.com/bigorg/other") == nil {
		t.Errorf("list %v, %d rows", o.List, len(o.Rows))
	}
}

func TestHoldKeepsChangesOutUntilReleased(t *testing.T) {
	_, ch, e := opsWorld(t)
	gone := t.TempDir() + "/nowhere"
	if !e.Hold(gone, "running x") || e.Writing() != 1 {
		t.Fatal("no hold")
	}
	e.Release(gone)
	idle(t, e)
	if e.Writing() != 0 || rowOf(e.Owner(), string(app)) == nil {
		t.Errorf("writing %d after release", e.Writing())
	}
	// A change asked for while another holds the clone is refused at once.
	if !e.Hold(string(app), "running x") {
		t.Fatal("no hold")
	}
	if busy := e.Fetch([]model.CloneKey{app}); len(busy) != 1 {
		t.Errorf("busy = %v", busy)
	}
	if busy := e.Fetch([]model.CloneKey{app}); len(busy) != 1 || len(ch.calls) != 0 {
		t.Errorf("fetched while held: %v", ch.calls)
	}
	e.Release(string(app))
	idle(t, e)
	if r := rowOf(e.Owner(), string(app)); r.Op.Doing != "" {
		t.Errorf("op after release = %+v", r.Op)
	}
}

func TestAQueuedChangeHoldsItsLane(t *testing.T) {
	_, ch, e := opsWorld(t)
	ch.block, ch.fetching = make(chan struct{}), make(chan struct{})
	e.Fetch([]model.CloneKey{app})
	if r := rowOf(e.Owner(), string(app)); r.Op.Doing == "" {
		t.Error("a queued fetch shows nothing")
	}
	if busy := e.Advance([]model.CloneKey{app}); len(busy) != 1 {
		t.Errorf("an update asked while a fetch is queued was not refused: %v", busy)
	}
	close(ch.block)
	idle(t, e)
}
