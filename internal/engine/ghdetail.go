package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
)

// keepGitHubDetails is how many repositories' GitHub details are held;
// each is small, and one dropped costs a point to read again. A constant:
// it bounds memory, not anything a user tunes.
const keepGitHubDetails = 256

// ghKey is what a repository's detail was read for: the heavy fields it
// follows, and when they were read. When one changes, or the heavy fields
// are read again, the detail is read again.
type ghKey struct {
	alerts  int
	oid, ci string
	at      time.Time
}

// ghEntry is a repository's detail, as read or tried.
type ghEntry struct {
	key ghKey
	d   model.GitHubDetail
}

// detailWant is what the counts of h ask to be read in detail: the alerts
// when there are some, and the checks when they failed or run.
func detailWant(h model.Heavy) github.DetailWant {
	var w github.DetailWant
	w.Alerts = h.Fields.Has(model.NeedAlerts) && h.Alerts > 0 && !h.AlertsOff && !h.AlertsDenied
	if h.Fields.Has(model.NeedCI) {
		switch h.CI {
		case "FAILURE", "ERROR", "PENDING", "EXPECTED":
			w.Oid = h.DefaultOid
		}
	}
	return w
}

// GitHubDetail is the GitHub detail of the repository k, of the owner
// shown: its open alerts and its checks that failed or run. It is read
// when the screen asks with read set (once the cursor has rested on the
// row), only when the counts ask for it, as a read gh tidy makes on its
// own (within the budget), and never in all; without read it says only
// what is held. What was read or failed is held: a failure is tried again
// after a while, and a detail is read again when the counts it follows
// are read again. An empty k says nothing is selected: a read queued for
// another row is dropped.
func (e *Engine) GitHubDetail(k model.RemoteKey, read bool) model.GitHubDetail {
	e.mu.Lock()
	e.ghLast = k
	o := e.owners[e.shown]
	if o == nil {
		e.mu.Unlock()
		return model.GitHubDetail{}
	}
	var h model.Heavy
	var at time.Time
	for _, r := range e.rowsByRemote(o, k) {
		if r.Heavy.Has && r.Heavy.Value.Found {
			h, at = r.Heavy.Value, r.Heavy.At
		}
	}
	want := detailWant(h)
	if !want.Alerts && want.Oid == "" {
		e.mu.Unlock()
		return model.GitHubDetail{}
	}
	if o.login == All {
		e.mu.Unlock()
		return model.GitHubDetail{State: model.DetailAll}
	}
	key := ghKey{h.Alerts, h.DefaultOid, h.CI, at}
	now := e.now()
	if ent := e.ghDetails[k]; ent != nil && ent.key == key {
		switch ent.d.State {
		case model.DetailRead, model.DetailReading:
			d := ent.d
			e.mu.Unlock()
			return d
		case model.DetailHeld, model.DetailFailed:
			if now.Sub(ent.d.At) < retryFailed {
				d := ent.d
				e.mu.Unlock()
				return d
			}
		}
	}
	if !read {
		e.mu.Unlock()
		return model.GitHubDetail{State: model.DetailReading}
	}
	ent := &ghEntry{key: key, d: model.GitHubDetail{State: model.DetailReading, At: now}}
	e.ghDetails[k] = ent
	e.evictGitHubDetails(k)
	gen, d := o.gen, ent.d
	e.mu.Unlock()
	e.queueGitHubDetail(o, gen, k, ent, want)
	return d
}

// evictGitHubDetails drops the oldest detail while more than the limit
// are held, never keep's. Called with e.mu held.
func (e *Engine) evictGitHubDetails(keep model.RemoteKey) {
	for len(e.ghDetails) > keepGitHubDetails {
		var oldest model.RemoteKey
		for k, ent := range e.ghDetails {
			if k != keep && (oldest == "" || ent.d.At.Before(e.ghDetails[oldest].d.At)) {
				oldest = k
			}
		}
		if oldest == "" {
			return
		}
		delete(e.ghDetails, oldest)
	}
}

// queueGitHubDetail reads one repository's detail. It runs only while
// the repository is still the one the screen asked for last, of the
// owner shown: moving the cursor down many rows reads only the last.
func (e *Engine) queueGitHubDetail(o *owner, gen int, k model.RemoteKey, ent *ghEntry, want github.DetailWant) {
	slug := slugOf(k)
	alive := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.ghLast == k && e.owners[e.shown] == o && o.gen == gen && e.ghDetails[k] == ent
	}
	t := e.track.add("GitHub", 1, "github", "details · "+slug, o.login)
	e.begin(o, "GitHub")
	e.apiPool.add(prioVisible, alive, func() {
		defer e.end(o, "GitHub")
		t.start()
		if ok, why := e.budget.allowAuto("graphql", true); !ok {
			e.log.Debug("detail held back", logging.KeyRepo, slug, logging.KeyReason, why)
			t.end("held back", false)
			e.setGitHubDetail(k, ent, model.GitHubDetail{State: model.DetailHeld, Why: why})
			return
		}
		d, rate, err := e.gh.RepoDetail(e.ctxOf(o), slug, want)
		e.budget.saw(rate, err == nil)
		e.track.spent(rate.Cost, 0)
		var rl *github.RateLimitError
		switch {
		case errors.Is(err, context.Canceled):
			t.end("", false)
			e.dropGitHubDetail(k, ent)
		case errors.As(err, &rl):
			e.budget.pause(rl)
			t.end("failed", true)
			e.setGitHubDetail(k, ent, model.GitHubDetail{State: model.DetailFailed, Why: "GitHub's rate limit"})
		case err != nil:
			e.log.Debug("detail not read", logging.KeyRepo, slug, logging.KeyReason, err.Error())
			t.end("failed", true)
			e.setGitHubDetail(k, ent, model.GitHubDetail{State: model.DetailFailed, Why: "GitHub did not answer"})
		default:
			t.end(fmt.Sprintf("%d alerts · %d checks", len(d.Alerts), len(d.Checks)), false)
			e.setGitHubDetail(k, ent, model.GitHubDetail{State: model.DetailRead, Value: d})
		}
	}, func() {
		t.end("", false)
		e.dropGitHubDetail(k, ent)
		e.end(o, "GitHub")
	})
}

// setGitHubDetail puts d in place of the read ent, unless another took its
// place meanwhile, and tells the screen.
func (e *Engine) setGitHubDetail(k model.RemoteKey, ent *ghEntry, d model.GitHubDetail) {
	e.mu.Lock()
	if e.ghDetails[k] == ent {
		d.At = e.now()
		ent.d = d
	}
	e.mu.Unlock()
	e.notify()
}

// dropGitHubDetail forgets the read ent, dropped before it ran, so that
// the next ask reads it.
func (e *Engine) dropGitHubDetail(k model.RemoteKey, ent *ghEntry) {
	e.mu.Lock()
	if e.ghDetails[k] == ent {
		delete(e.ghDetails, k)
	}
	e.mu.Unlock()
}

// retryGitHubDetail forgets what held back or failed the detail of the
// repository asked for last, once its wait is over, so that the screen
// asks for it again. Only that one: the others are read again when they
// are asked for.
func (e *Engine) retryGitHubDetail() {
	e.mu.Lock()
	ent := e.ghDetails[e.ghLast]
	again := ent != nil && (ent.d.State == model.DetailHeld || ent.d.State == model.DetailFailed) && e.now().Sub(ent.d.At) >= retryFailed
	if again {
		delete(e.ghDetails, e.ghLast)
	}
	e.mu.Unlock()
	if again {
		e.notify()
	}
}
