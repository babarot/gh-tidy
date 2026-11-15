package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
)

func countOf(a model.Activity, name string) model.Count {
	for _, c := range a.Counts {
		if c.Name == name {
			return c
		}
	}
	return model.Count{}
}

// A run starts with the first work queued and ends when none is left; its
// counts reach their totals and the last tasks say how they went.
func TestActivityFollowsARun(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.add("me", "tool", "me/tool", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	hold, held := make(chan struct{}), make(chan struct{})
	w.local.mu.Lock()
	w.local.hold, w.local.held = map[string]chan struct{}{"/src/github.com/me/app": hold}, held
	w.local.mu.Unlock()
	e := w.engine()
	e.Open("me")
	<-held
	a := e.Activity()
	if !a.Busy() || a.Verb != "Reading" || a.What != "me" || countOf(a, "clones").Total == 0 {
		t.Fatalf("while reading: %+v", a)
	}
	if !slices.ContainsFunc(a.Running, func(x model.Task) bool { return x.Kind == "clone" && x.Name == "app" }) {
		t.Errorf("running = %+v", a.Running)
	}
	close(hold)
	idle(t, e)
	a = e.Activity()
	if a.Busy() || a.Ended.IsZero() {
		t.Fatalf("after: %+v", a)
	}
	// Both clones, and any measured again on GitHub's default branch.
	if countOf(a, "clones").Total < 2 {
		t.Errorf("clones = %+v", countOf(a, "clones"))
	}
	for _, c := range a.Counts {
		if c.Done != c.Total {
			t.Errorf("%s %d/%d", c.Name, c.Done, c.Total)
		}
	}
	if !slices.ContainsFunc(a.Done, func(x model.Task) bool {
		return x.Kind == "github" && strings.HasPrefix(x.Name, "fields") && x.Note == "cost 1"
	}) {
		t.Errorf("done = %+v", a.Done)
	}
	if limits := []int{a.Workers[0].Limit, a.Workers[1].Limit, a.Workers[2].Limit}; limits[1] != networkAtOnce || limits[2] != apiAtOnce {
		t.Errorf("workers = %+v", a.Workers)
	}
	if !a.Rate.Known || a.Rate.Limit != 5000 || a.Rate.Used == 0 {
		t.Errorf("rate = %+v", a.Rate)
	}
}

// What the reads asked for spend is the load's; what gh tidy reads on its
// own later is counted apart. A list unchanged saves its pages.
func TestRateCountsLoadsApart(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	e := w.engine()
	e.Open("me")
	idle(t, e)
	if r := e.Activity().Rate; r.Used == 0 || r.UnaskedUsed != 0 {
		t.Fatalf("after opening: %+v", r)
	}
	w.clock.add(listFresh + time.Minute)
	e.Tick()
	idle(t, e)
	r := e.Activity().Rate
	if r.UnaskedSaved != 1 {
		t.Errorf("an unchanged list read unasked saved %d pages", r.UnaskedSaved)
	}
	e.ReadList()
	idle(t, e)
	if r := e.Activity().Rate; r.Saved != 1 || r.Used != 0 {
		t.Errorf("L: the load starts from zero and saves the page: %+v", r)
	}
}

// r reads every field again, fresh or not.
func TestRefreshReadsEverything(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	e := w.engine()
	e.Open("me")
	idle(t, e)
	scans, asked := w.local.scans, len(w.gh.asked)
	e.Tick()
	idle(t, e)
	if len(w.gh.asked) != asked {
		t.Fatal("fresh fields were read again unasked")
	}
	e.Refresh()
	idle(t, e)
	if len(w.gh.asked) == asked || w.local.scans == scans {
		t.Errorf("r did not read again: %d asked, %d scans", len(w.gh.asked)-asked, w.local.scans-scans)
	}
}

// A failure is said by its kind, never by the error's text.
func TestAFailureKeepsItsTextOut(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.listErr = errors.New("Get https://token@example.com/secret: refused")
	e := w.engine()
	e.Open("me")
	idle(t, e)
	for _, x := range e.Activity().Done {
		if strings.Contains(x.Note, "example.com") || strings.Contains(x.Note, "refused") {
			t.Errorf("a note holds an error's text: %+v", x)
		}
		if x.Kind == "github" && strings.HasPrefix(x.Name, "list") && (x.Note != "failed" || !x.Failed) {
			t.Errorf("list note = %+v", x)
		}
	}
	_ = context.Background
}
