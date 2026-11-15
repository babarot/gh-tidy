package engine

import (
	"log/slog"
	"sync"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/logging"
)

// The share of the hourly points left below which reads gh-tidy makes on
// its own stop: first the heavy fields, then everything. The token is
// shared with other tools, so gh-tidy leaves them most of it. ownPerHour
// bounds what gh-tidy itself spends unasked in an hour, however much is
// left. Constants: they guard the shared limit, which no user should need
// to tune.
const (
	heavyFloor = 0.30
	allFloor   = 0.10
	ownPerHour = 600
)

// budget follows the rate limit across responses and decides what may be
// read unasked. REST (core) and GraphQL have limits of their own. GitHub's
// regions report the remaining points out of step, so the lowest count
// seen for a reset is used.
type budget struct {
	mu  sync.Mutex
	now func() time.Time
	log *slog.Logger

	pools   map[string]*points // by resource: core, graphql
	spent   int
	spentAt time.Time // the start of the hour spent counts

	pausedUntil time.Time
	strikes     int
	batch       int // 0 until a timeout shrinks it
}

// points is one resource's limit as last seen.
type points struct {
	limit, lowest int
	reset         time.Time
}

func newBudget(now func() time.Time, log *slog.Logger) *budget {
	return &budget{now: now, log: log, pools: map[string]*points{}}
}

// saw takes what a response said of the rate limit, and what it cost. ok
// says the request was answered: only then does a run of refusals end.
func (b *budget) saw(r github.Rate, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.Sub(b.spentAt) > time.Hour {
		b.spent, b.spentAt = 0, now
	}
	b.spent += r.Cost
	if ok {
		b.strikes = 0
	}
	if !r.Known() {
		return
	}
	res := r.Resource
	if res == "" {
		res = "core"
	}
	p := b.pools[res]
	if p == nil || !r.Reset.Equal(p.reset) {
		b.pools[res] = &points{limit: r.Limit, lowest: r.Remaining, reset: r.Reset}
		return
	}
	p.lowest = min(p.lowest, r.Remaining)
}

// allowAuto says whether a read gh-tidy makes on its own, of resource,
// may go now, and why not when it may not.
func (b *budget) allowAuto(resource string, heavy bool) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	switch {
	case now.Before(b.pausedUntil):
		return false, "waiting out a GitHub rate limit until " + b.pausedUntil.Format("15:04:05")
	case b.spent >= ownPerHour && now.Sub(b.spentAt) <= time.Hour:
		return false, "gh tidy used its share of the hour"
	}
	p := b.pools[resource]
	if p == nil || p.limit == 0 || now.After(p.reset) {
		return true, ""
	}
	left := float64(p.lowest) / float64(p.limit)
	switch {
	case left < allFloor:
		return false, "few GitHub points left until " + p.reset.Format("15:04")
	case heavy && left < heavyFloor:
		return false, "GitHub points kept for other tools until " + p.reset.Format("15:04")
	}
	return true, ""
}

// pause stops every request after GitHub refused one for a rate limit:
// as long as GitHub says, longer each time it happens again.
func (b *budget) pause(e *github.RateLimitError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	wait := e.Wait() << min(b.strikes, 4)
	b.pausedUntil = b.now().Add(wait)
	b.strikes++
	if e.Primary {
		res := e.Rate.Resource
		if res == "" {
			res = "core"
		}
		b.pools[res] = &points{limit: max(e.Rate.Limit, 1), lowest: 0, reset: e.Reset}
	}
	b.log.Info("github rate limit: holding back", "primary", e.Primary, logging.KeyWaitedMS, wait.Milliseconds())
}

// Paused is when requests may go again; zero when they may now.
func (b *budget) Paused() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.now().Before(b.pausedUntil) {
		return b.pausedUntil
	}
	return time.Time{}
}

// shrink halves the batch after GitHub gave up on one, for the rest of
// the run.
func (b *budget) shrink() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.batch == 0 {
		b.batch = heavyBatch
	}
	b.batch = max(1, b.batch/2)
	b.log.Info("github gave up on a query: asking for fewer at a time", "batch", b.batch)
}

func (b *budget) batchSize(def int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.batch == 0 {
		return def
	}
	return b.batch
}
