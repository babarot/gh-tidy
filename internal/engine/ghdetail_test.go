package engine

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
)

// detailWorld is an owner with a repository with alerts and failing
// checks (app), one with nothing to read (tool), and one more with
// alerts (web), their counts read.
func detailWorld(t *testing.T) (*world, *Engine) {
	w := newWorld(t)
	w.gh.setList("me", github.Complete, "e1", "app", "tool", "web")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", Alerts: 2, CI: "FAILURE"}
	w.gh.heavy["me/tool"] = github.HeavyRepo{Default: "main", DefaultOid: "t1", CI: "SUCCESS"}
	w.gh.heavy["me/web"] = github.HeavyRepo{Default: "main", DefaultOid: "w1", Alerts: 1}
	w.gh.details = map[string]model.RepoDetail{"me/app": {Alerts: []model.Alert{{Package: "lodash", Severity: "critical"}}, AlertsTotal: 1,
		Checks: []model.Check{{Name: "test"}}}}
	e := w.engine()
	e.Open("me")
	e.SetNeeds(model.NeedAlerts|model.NeedCI|model.NeedNotCloned, nil)
	idle(t, e)
	return w, e
}

const appR, toolR, webR = model.RemoteKey("github.com/me/app"), model.RemoteKey("github.com/me/tool"), model.RemoteKey("github.com/me/web")

func detailed(w *world) []string {
	w.gh.mu.Lock()
	defer w.gh.mu.Unlock()
	return slices.Clone(w.gh.detailed)
}

// A repository selected is read once, with the parts its counts ask for,
// and held; a repository with nothing to read is not read.
func TestTheDetailIsReadOnceWhenSelected(t *testing.T) {
	w, e := detailWorld(t)
	if d := e.GitHubDetail(appR, true); d.State != model.DetailReading {
		t.Fatalf("first ask: %+v", d)
	}
	idle(t, e)
	d := e.GitHubDetail(appR, true)
	if d.State != model.DetailRead || len(d.Value.Alerts) != 1 || !d.Value.AlertsAsked || !d.Value.ChecksAsked {
		t.Fatalf("after reading: %+v", d)
	}
	e.GitHubDetail(appR, true)
	idle(t, e)
	if d := e.GitHubDetail(toolR, true); d.State != model.DetailNone {
		t.Errorf("a repository with nothing to read: %+v", d)
	}
	idle(t, e)
	if got := detailed(w); !slices.Equal(got, []string{"me/app"}) {
		t.Errorf("read %q; want app once", got)
	}
	if d := e.GitHubDetail(webR, true); d.State != model.DetailReading {
		t.Errorf("web: %+v", d)
	}
	idle(t, e)
	if d := e.GitHubDetail(webR, true); d.Value.ChecksAsked {
		t.Error("asked for the checks of a repository whose checks pass")
	}
}

// When the counts it follows change, the detail is read again.
func TestTheDetailIsReadAgainWhenTheCountsChange(t *testing.T) {
	w, e := detailWorld(t)
	e.GitHubDetail(appR, true)
	idle(t, e)
	w.gh.mu.Lock()
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", Alerts: 2, CI: "PENDING"}
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	e.GitHubDetail(appR, true)
	idle(t, e)
	if got := detailed(w); len(got) != 2 {
		t.Errorf("read %q; want app again after its checks changed", got)
	}
}

// A detail held back or failed is not asked again at once, however often
// the screen asks; Tick tries it again after a while.
func TestAFailedDetailIsNotAskedAgainAtOnce(t *testing.T) {
	w, e := detailWorld(t)
	w.gh.mu.Lock()
	w.gh.detailErr = errors.New("502")
	w.gh.mu.Unlock()
	e.GitHubDetail(appR, true)
	idle(t, e)
	for range 5 {
		if d := e.GitHubDetail(appR, true); d.State != model.DetailFailed || d.Why == "" {
			t.Fatalf("after a failure: %+v", d)
		}
		idle(t, e)
	}
	if got := detailed(w); len(got) != 1 {
		t.Fatalf("read %d times; want once", len(got))
	}
	w.gh.mu.Lock()
	w.gh.detailErr = nil
	w.gh.mu.Unlock()
	w.clock.add(retryFailed + time.Second)
	e.Tick()
	idle(t, e)
	e.GitHubDetail(appR, true)
	idle(t, e)
	if d := e.GitHubDetail(appR, true); d.State != model.DetailRead {
		t.Errorf("after the wait: %+v", d)
	}
}

// Below the budget's floor the detail is held back, and says why.
func TestADetailIsHeldBackByTheBudget(t *testing.T) {
	w, e := detailWorld(t)
	// An asked read (r) brings word that few points are left.
	w.gh.mu.Lock()
	w.gh.rate.Remaining = 100 // of 5000
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	e.GitHubDetail(appR, true)
	idle(t, e)
	if d := e.GitHubDetail(appR, true); d.State != model.DetailHeld || d.Why == "" {
		t.Errorf("below the floor: %+v", d)
	}
	if got := detailed(w); len(got) != 0 {
		t.Errorf("read %q below the floor", got)
	}
}

// Rows the cursor only passes are not read: the screen asks without
// read until the cursor rests. A read queued for a row is dropped when the
// cursor moves on, even to a row with nothing to read.
func TestRowsOnlyPassedAreNotRead(t *testing.T) {
	w, e := detailWorld(t)
	e.GitHubDetail(appR, false)
	e.GitHubDetail(webR, false)
	e.GitHubDetail(toolR, false)
	idle(t, e)
	if got := detailed(w); len(got) != 0 {
		t.Fatalf("read %q for rows only passed", got)
	}

	w.gh.mu.Lock()
	w.gh.heavy["me/api"] = github.HeavyRepo{Default: "main", DefaultOid: "p1", Alerts: 3}
	w.gh.details["me/web"] = model.RepoDetail{}
	release := make(chan struct{})
	w.gh.detailGo = release
	w.gh.mu.Unlock()
	w.gh.setList("me", github.Complete, "e2", "app", "tool", "web", "api")
	e.Refresh()
	idle(t, e)
	// Both workers busy, waiting: each read is held in the fake before
	// the cursor moves on.
	waitFor := func(n int) {
		for deadline := time.Now().Add(5 * time.Second); ; {
			w.gh.mu.Lock()
			got := w.gh.waiting
			w.gh.mu.Unlock()
			if got >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d reads waiting; want %d", got, n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	e.GitHubDetail(appR, true)
	waitFor(1)
	e.GitHubDetail(webR, true)
	waitFor(2)
	e.GitHubDetail("github.com/me/api", true) // queued behind them
	e.GitHubDetail(toolR, true)               // nothing to read here
	close(release)
	idle(t, e)
	if got := detailed(w); slices.Contains(got, "me/api") {
		t.Errorf("read %q; api was left before its turn", got)
	}
}

// all reads no more of GitHub: its rows say so, and nothing is read.
func TestAllReadsNoDetail(t *testing.T) {
	w, e := detailWorld(t)
	w.local.add("me", "app", "me/app", nil)
	e.Open("me")
	idle(t, e)
	e.Open(All)
	idle(t, e)
	if d := e.GitHubDetail(appR, true); d.State != model.DetailAll {
		t.Errorf("in all: %+v", d)
	}
	idle(t, e)
	if got := detailed(w); len(got) != 0 {
		t.Errorf("all read %q", got)
	}
}
