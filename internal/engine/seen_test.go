package engine

import (
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
)

var (
	ghNow  = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) // GitHub's clock at the first read
	before = ghNow.Add(-24 * time.Hour)
)

// newWorldWith is an owner whose app has two open issues made before the
// first read, read with their creation times.
func newWorldWith(t *testing.T) (*world, *Engine) {
	w := newWorld(t)
	w.gh.rate.Date = ghNow
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", ID: 42, Issues: 2,
		IssuesNewest: []time.Time{before, before.Add(-time.Hour)}}
	w.gh.details = map[string]model.RepoDetail{"me/app": {Issues: []model.Opened{{Number: 2, Title: "x", Created: before}}}}
	e := w.engine()
	e.Open("me")
	e.SetNeeds(model.NeedCounts|model.NeedNewest|model.NeedNotCloned, nil)
	idle(t, e)
	return w, e
}

func newOfApp(e *Engine) model.New { return rowOf(e.Owner(), "github.com/me/app").New }

// What was made before an owner was first read is not new; what is made
// after is, by GitHub's clock.
func TestNewIsWhatCameAfterTheFirstRead(t *testing.T) {
	w, e := newWorldWith(t)
	if n := newOfApp(e); !n.Known || n.Issues != 0 {
		t.Fatalf("at the first read: %+v", n)
	}
	w.gh.mu.Lock()
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", ID: 42, Issues: 3,
		IssuesNewest: []time.Time{ghNow.Add(time.Hour), before, before.Add(-time.Hour)}}
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	if n := newOfApp(e); !n.Known || n.Issues != 1 {
		t.Errorf("after an issue was opened: %+v", n)
	}
}

// Without GitHub's clock there is no time to tell new ones by: not known,
// never shown as none new; a value read without the creation times is
// not known either.
func TestNewIsNotKnownWithoutABaseline(t *testing.T) {
	w := newWorld(t)
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", ID: 42, Issues: 2, IssuesNewest: []time.Time{before}}
	e := w.engine()
	e.Open("me")
	e.SetNeeds(model.NeedCounts|model.NeedNewest|model.NeedNotCloned, nil)
	idle(t, e)
	if n := newOfApp(e); n.Known {
		t.Errorf("no Date from GitHub: %+v", n)
	}
	// The next read that brings GitHub's clock sets it.
	w.gh.mu.Lock()
	w.gh.rate.Date = ghNow
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	if n := newOfApp(e); !n.Known || n.Issues != 0 {
		t.Errorf("after a read with GitHub's clock: %+v", n)
	}
}

// Resting on a row with its titles shown moves the time seen to the
// newest shown; its marks stay until the cursor leaves the row.
func TestSeenMovesWhenTheTitlesAreShown(t *testing.T) {
	w, e := newWorldWith(t)
	w.gh.mu.Lock()
	opened := ghNow.Add(time.Hour)
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", ID: 42, Issues: 3,
		IssuesNewest: []time.Time{opened, before, before.Add(-time.Hour)}}
	w.gh.details["me/app"] = model.RepoDetail{Issues: []model.Opened{{Number: 3, Title: "new", Created: opened}, {Number: 2, Title: "x", Created: before}}}
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	k := model.RemoteKey("github.com/me/app")
	e.GitHubDetail(k, true)
	idle(t, e)
	e.MarkSeen(k)
	idle(t, e)
	if n := newOfApp(e); n.Issues != 1 {
		t.Errorf("while resting, the mark went: %+v", n)
	}
	e.GitHubDetail("", false) // the cursor leaves
	idle(t, e)
	if n := newOfApp(e); !n.Known || n.Issues != 0 {
		t.Errorf("after leaving: %+v", n)
	}
	if got, _ := w.store.LoadSeen(); !got.Repos["github.com/42"].Equal(opened) {
		t.Errorf("seen kept = %+v; want the newest shown, by GitHub's clock", got.Repos)
	}
	// Seen is kept by GitHub's number for the repository: a rename keeps it.
	e2 := w.engine()
	e2.Open("me")
	e2.SetNeeds(model.NeedCounts|model.NeedNewest|model.NeedNotCloned, nil)
	idle(t, e2)
	if n := rowOf(e2.Owner(), "github.com/me/app").New; !n.Known || n.Issues != 0 {
		t.Errorf("at the next start: %+v", n)
	}
}

// Seen does not move when only the issues were read, nor without the
// cursor resting (MarkSeen is not called), nor in all.
func TestSeenStaysWhenNotAllWasShown(t *testing.T) {
	w, e := newWorldWith(t)
	opened := ghNow.Add(time.Hour)
	w.gh.mu.Lock()
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "a1", ID: 42, Issues: 3,
		IssuesNewest: []time.Time{opened, before, before.Add(-time.Hour)}}
	w.gh.details["me/app"] = model.RepoDetail{Issues: []model.Opened{{Number: 3, Created: opened}}}
	w.gh.prsFail = map[string]string{"me/app": "error"}
	w.gh.mu.Unlock()
	e.Refresh()
	idle(t, e)
	k := model.RemoteKey("github.com/me/app")
	e.GitHubDetail(k, true)
	idle(t, e)
	e.MarkSeen(k)
	e.GitHubDetail("", false)
	idle(t, e)
	if n := newOfApp(e); n.Issues != 1 {
		t.Errorf("seen moved with the pull requests not read: %+v", n)
	}
	e.Open(All)
	idle(t, e)
	e.MarkSeen(k)
	idle(t, e)
	if got, _ := w.store.LoadSeen(); len(got.Repos) != 0 {
		t.Errorf("seen kept: %+v", got.Repos)
	}
}
