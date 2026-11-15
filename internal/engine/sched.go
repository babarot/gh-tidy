package engine

import (
	"context"
	"slices"
	"sync"
)

// Priorities, highest first: what the user asked for, then the rows on the
// screen, then the rest of the owner shown.
const (
	prioAsked = iota
	prioVisible
	prioRest
)

// task is one piece of work for a pool. alive says, when its turn comes,
// whether it is still wanted (its owner may have been left since); dropped
// runs instead of run when it is not.
type task struct {
	prio    int
	seq     int64
	alive   func() bool
	run     func()
	dropped func()
}

// pool runs tasks of one kind of outside access, at most n at once, in
// priority order and, within a priority, in the order they came.
type pool struct {
	mu      sync.Mutex
	n       int
	running int
	queue   []task
	seq     int64
	idle    *idleGroup
}

func newPool(n int, idle *idleGroup) *pool { return &pool{n: max(1, n), idle: idle} }

// add queues run, and starts a worker when one is free. dropped, when not
// nil, runs instead of run if alive says no when its turn comes.
func (p *pool) add(prio int, alive func() bool, run, dropped func()) {
	p.mu.Lock()
	p.seq++
	p.queue = append(p.queue, task{prio: prio, seq: p.seq, alive: alive, run: run, dropped: dropped})
	slices.SortStableFunc(p.queue, func(a, b task) int {
		if a.prio != b.prio {
			return a.prio - b.prio
		}
		return int(a.seq - b.seq)
	})
	p.idle.add(1)
	start := p.running < p.n
	if start {
		p.running++
	}
	p.mu.Unlock()
	if start {
		go p.work()
	}
}

func (p *pool) work() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.running--
			p.mu.Unlock()
			return
		}
		t := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()
		if t.alive == nil || t.alive() {
			t.run()
		} else if t.dropped != nil {
			t.dropped()
		}
		p.idle.add(-1)
	}
}

// drop removes the queued tasks that are no longer wanted.
func (p *pool) drop() {
	p.mu.Lock()
	kept := p.queue[:0]
	var gone []task
	for _, t := range p.queue {
		if t.alive == nil || t.alive() {
			kept = append(kept, t)
		} else {
			gone = append(gone, t)
		}
	}
	p.queue = kept
	p.mu.Unlock()
	for _, t := range gone {
		if t.dropped != nil {
			t.dropped()
		}
	}
	p.idle.add(-len(gone))
}

// idleGroup counts the work queued or running in every pool, so that a
// test can wait until none is left.
type idleGroup struct {
	mu   sync.Mutex
	n    int
	cond *sync.Cond
}

func newIdleGroup() *idleGroup {
	g := &idleGroup{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *idleGroup) add(d int) {
	g.mu.Lock()
	g.n += d
	if g.n == 0 {
		g.cond.Broadcast()
	}
	g.mu.Unlock()
}

// wait returns when no work is left, or ctx ends.
func (g *idleGroup) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		g.mu.Lock()
		for g.n > 0 && ctx.Err() == nil {
			g.cond.Wait()
		}
		g.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return ctx.Err()
	case <-ctx.Done():
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
		<-done
		return ctx.Err()
	}
}
