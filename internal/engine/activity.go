package engine

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

// keepDone is how many tasks done a run keeps, for the screen to show the
// last few. A constant: it bounds memory, not anything a user tunes.
const keepDone = 8

// tracker follows the work of a run (see model.Activity), and what reads
// spent of GitHub's rate limit.
type tracker struct {
	mu  sync.Mutex
	now func() time.Time

	seq  int
	run  model.Activity
	live map[int]model.Task
	open int // tasks queued or running

	// asked says a read was asked for (an owner opened, r, L) and the run
	// it is in has not ended: what reads spend then is that load's.
	asked  bool
	last   time.Time // when the last task ended
	used   [2]int    // asked, unasked
	saved  [2]int
	asking int
	lastAt time.Time
}

func newTracker(now func() time.Time) *tracker {
	return &tracker{now: now, live: map[int]model.Task{}}
}

// active reports whether a run goes on. Called with t.mu held.
func (t *tracker) active() bool { return !t.run.Started.IsZero() && t.run.Ended.IsZero() }

// idled ends the run when the pools have nothing left and no task is
// open: a task that ends may queue the next before its pool lets go.
func (t *tracker) idled() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active() && t.open == 0 {
		t.run.Ended = t.now()
		t.asked = false
	}
}

// verbs say what a run does, by the kind of its first task.
var verbs = map[string]string{"clone": "Reading", "github": "Reading", "fetch": "Fetching", "pull": "Pulling", "cleanup": "Cleaning up", "create": "Cloning"}

// tracked is a piece of work followed from when it is queued.
type tracked struct {
	t      *tracker
	id     int
	group  string
	weight int
	kind   string
	name   string
	ended  bool
}

// add queues a task of kind, named name, counting weight in group; who is
// the owner shown, for a run that starts with it.
func (t *tracker) add(group string, weight int, kind, name, who string) *tracked {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active() {
		t.run = model.Activity{Verb: verbs[kind], What: who, Started: t.now()}
		clear(t.live)
		t.asked = false
	}
	t.open++
	i := slices.IndexFunc(t.run.Counts, func(c model.Count) bool { return c.Name == group })
	if i < 0 {
		t.run.Counts = append(t.run.Counts, model.Count{Name: group})
		i = len(t.run.Counts) - 1
	}
	t.run.Counts[i].Total += weight
	t.seq++
	return &tracked{t: t, id: t.seq, group: group, weight: weight, kind: kind, name: name}
}

// start marks the task running.
func (k *tracked) start() {
	t := k.t
	t.mu.Lock()
	defer t.mu.Unlock()
	t.live[k.id] = model.Task{Kind: k.kind, Name: k.name, Start: t.now()}
	if k.kind == "github" {
		t.asking++
	}
}

// end marks the task done, with how it went. A task dropped before it ran
// ends too, with no note, so that a run's counts reach their totals.
func (k *tracked) end(note string, failed bool) {
	t := k.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if k.ended {
		return
	}
	k.ended = true
	now := t.now()
	if x, ok := t.live[k.id]; ok {
		delete(t.live, k.id)
		x.Note, x.Failed, x.End = note, failed, now
		t.run.Done = append(t.run.Done, x)
		if len(t.run.Done) > keepDone {
			t.run.Done = t.run.Done[len(t.run.Done)-keepDone:]
		}
		if k.kind == "github" {
			t.asking--
			t.lastAt = now
		}
	}
	for i := range t.run.Counts {
		if t.run.Counts[i].Name == k.group {
			t.run.Counts[i].Done += k.weight
		}
	}
	t.open--
	t.last = now
}

// load says a read of who was asked for: what reads spend from now until
// the run ends is that load's, and what gh tidy reads on its own after is
// counted from zero too. It starts a run when none goes on; the caller
// holds the pools busy while it queues, so that a load that queues
// nothing still ends.
func (t *tracker) load(who string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.used, t.saved = [2]int{}, [2]int{}
	if !t.active() {
		t.run = model.Activity{Verb: "Reading", What: who, Started: t.now()}
		clear(t.live)
	}
	t.asked = true
}

// spent records what a response cost and how many pages answered 304.
func (t *tracker) spent(cost, saved int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	i := 1
	if t.asked {
		i = 0
	}
	t.used[i] += cost
	t.saved[i] += saved
}

// Activity is what gh-tidy is doing, or did last.
func (e *Engine) Activity() model.Activity {
	t := e.track
	t.mu.Lock()
	a := t.run
	a.Counts = slices.Clone(a.Counts)
	a.Done = slices.Clone(a.Done)
	a.Running = nil
	// In the order queued where two started at once, so that the screen
	// does not reorder them from one frame to the next.
	for _, id := range slices.Sorted(maps.Keys(t.live)) {
		a.Running = append(a.Running, t.live[id])
	}
	slices.SortStableFunc(a.Running, func(x, y model.Task) int { return x.Start.Compare(y.Start) })
	a.Rate.Used, a.Rate.Saved, a.Rate.UnaskedUsed, a.Rate.UnaskedSaved = t.used[0], t.saved[0], t.used[1], t.saved[1]
	a.Rate.Asking, a.Rate.LastAsked = t.asking > 0, t.lastAt
	t.mu.Unlock()
	a.Rate.Known, a.Rate.Remaining, a.Rate.Limit, a.Rate.Reset, a.Rate.Resource = e.budget.lowest()
	for _, p := range []struct {
		name string
		p    *pool
	}{{"disk", e.localPool}, {"net", e.netPool}, {"api", e.apiPool}} {
		running, queued := p.p.stats()
		a.Workers = append(a.Workers, model.Workers{Name: p.name, Running: running, Limit: p.p.n})
		a.Queued += queued
	}
	return a
}

// busy holds the pools busy while work is queued from outside them, so
// that a task ending at once does not end the run before the rest is in;
// it returns what lets go.
func (e *Engine) busy() func() {
	e.idle.add(1)
	return func() { e.idle.add(-1) }
}
