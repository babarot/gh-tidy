package policy

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

func TestDecideAdvance(t *testing.T) {
	ok := model.AdvanceFacts{Default: "main", DefaultSource: model.DefaultFromGitHub, LocalExists: true, RemoteExists: true,
		Upstream: "origin/main", Behind: 3, FastForward: true}
	with := func(edit func(*model.AdvanceFacts)) model.AdvanceFacts { f := ok; edit(&f); return f }
	for _, c := range []struct {
		name   string
		f      model.AdvanceFacts
		action AdvanceAction
		why    string // part of Why, for a skip
	}{
		{"checked out nowhere", ok, AdvanceRef, ""},
		{"from origin/HEAD", with(func(f *model.AdvanceFacts) { f.DefaultSource = model.DefaultFromOriginHEAD }), AdvanceRef, ""},
		{"checked out and clean", with(func(f *model.AdvanceFacts) { f.CheckedOut = "/wt" }), AdvanceMerge, ""},
		{"up to date", with(func(f *model.AdvanceFacts) { f.Behind = 0 }), AdvanceCurrent, ""},
		{"default unknown", with(func(f *model.AdvanceFacts) { f.Default = "" }), AdvanceSkip, "unknown"},
		{"default guessed", with(func(f *model.AdvanceFacts) { f.DefaultSource = model.DefaultFromGuess }), AdvanceSkip, "guessed"},
		{"no origin branch", with(func(f *model.AdvanceFacts) { f.RemoteExists = false }), AdvanceSkip, "does not exist"},
		{"no local branch: never create one", with(func(f *model.AdvanceFacts) { f.LocalExists = false }), AdvanceSkip, "no local main"},
		{"tracks another remote", with(func(f *model.AdvanceFacts) { f.Upstream = "upstream/main" }), AdvanceSkip, "tracks upstream/main"},
		{"tracks nothing", with(func(f *model.AdvanceFacts) { f.Upstream = "" }), AdvanceSkip, "tracks nothing"},
		{"not a fast-forward", with(func(f *model.AdvanceFacts) { f.FastForward = false }), AdvanceSkip, "rebase or merge"},
		{"changes where checked out", with(func(f *model.AdvanceFacts) { f.CheckedOut, f.Dirty = "/wt", 2 }), AdvanceSkip, "2 uncommitted changes"},
		{"unreadable where checked out", with(func(f *model.AdvanceFacts) { f.CheckedOut, f.Dirty = "/wt", -1 }), AdvanceSkip, "could not be read"},
		{"rebase where checked out", with(func(f *model.AdvanceFacts) { f.CheckedOut, f.InProgress, f.Holders = "/wt", "rebase", 1 }), AdvanceSkip, "rebase is in progress"},
		{"checked out in two worktrees", with(func(f *model.AdvanceFacts) { f.CheckedOut, f.Holders = "/wt", 2 }), AdvanceSkip, "in 2 worktrees"},
		{"held by a missing worktree", with(func(f *model.AdvanceFacts) { f.Holders, f.HeldByMissing = 1, true }), AdvanceSkip, "cannot find"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := DecideAdvance(c.f)
			if got.Action != c.action {
				t.Fatalf("action = %v (%s), want %v", got.Action, got.Why, c.action)
			}
			if c.why != "" && !strings.Contains(got.Why, c.why) {
				t.Errorf("why = %q, want it to say %q", got.Why, c.why)
			}
			if got.Action == AdvanceMerge && got.Worktree != "/wt" {
				t.Errorf("merge runs in %q", got.Worktree)
			}
			if (got.Action == AdvanceRef || got.Action == AdvanceMerge) && got.Commits != 3 {
				t.Errorf("commits = %d, want 3", got.Commits)
			}
		})
	}
}

// cleanupClone is a clone with every kind of worktree and branch d judges.
func cleanupClone() (model.Clone, model.CleanupFacts) {
	wt := func(path, branch string, edit func(*model.Worktree)) model.Worktree {
		w := model.Worktree{Path: path, Branch: branch}
		if edit != nil {
			edit(&w)
		}
		return w
	}
	c := model.Clone{Path: "/app", Default: "main", Status: model.Status{Head: "main"},
		Worktrees: []model.Worktree{
			{Path: "/app", Branch: "main", Main: true},
			wt("/wt/landed", "landed", nil),
			wt("/wt/landed-dirty", "landed-dirty", func(w *model.Worktree) { w.Dirty = 1 }),
			wt("/wt/here", "here", nil),
			wt("/wt/fresh", "fresh", nil),
			wt("/wt/working", "working", nil),
			wt("/wt/locked", "locked", func(w *model.Worktree) { w.Locked = true }),
			wt("/wt/rebasing", "rebasing", func(w *model.Worktree) { w.InProgress = "rebase" }),
			wt("/wt/vanished", "vanished", func(w *model.Worktree) { w.Prunable = true }),
		},
		Branches: []model.Branch{{Name: "main"}, {Name: "landed"}, {Name: "landed-dirty"}, {Name: "here"}, {Name: "fresh"}, {Name: "working"},
			{Name: "locked"}, {Name: "rebasing"}, {Name: "vanished"}, {Name: "merged-branch"}, {Name: "keep-me"},
			{Name: "squashed", Upstream: "origin/squashed", Gone: true}, {Name: "unmerged"}},
	}
	f := model.CleanupFacts{Base: "origin/main",
		Merged:   map[string]bool{"main": true, "landed": true, "landed-dirty": true, "here": true, "fresh": true, "locked": true, "rebasing": true, "vanished": true, "merged-branch": true, "keep-me": true},
		WorkedOn: map[string]bool{"landed": true, "landed-dirty": true, "here": true, "working": true, "locked": true, "rebasing": true, "merged-branch": true, "keep-me": true, "squashed": true, "unmerged": true},
	}
	return c, f
}

func names(items []Item) []string {
	var out []string
	for _, it := range items {
		n := it.Branch
		if it.Kind == ItemPrunable {
			n = "prunable:" + it.Branch
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestDecideCleanup(t *testing.T) {
	c, f := cleanupClone()
	p := DecideCleanup(c, f, Cleanup{Protect: []string{"keep-me"}}, "/wt/here/sub")
	if got, want := names(p.Remove), []string{"landed", "merged-branch", "prunable:vanished"}; !slices.Equal(got, want) {
		t.Errorf("remove = %v, want %v", got, want)
	}
	if got, want := names(p.Keep), []string{"fresh", "here", "landed-dirty", "locked", "rebasing", "squashed"}; !slices.Equal(got, want) {
		t.Errorf("keep = %v, want %v", got, want)
	}
	why := map[string]string{}
	for _, k := range p.Keep {
		why[k.Branch] = k.Why
	}
	for b, want := range map[string]string{"fresh": "no commits", "here": "you are in it", "landed-dirty": "1 uncommitted", "locked": "locked", "rebasing": "rebase", "squashed": "delete_gone"} {
		if !strings.Contains(why[b], want) {
			t.Errorf("%s kept for %q, want it to say %q", b, why[b], want)
		}
	}
	// working and unmerged are not merged: neither removed nor listed.
	if slices.Contains(names(p.Remove), "working") || slices.Contains(names(p.Remove), "unmerged") {
		t.Error("a branch with work of its own is removed")
	}

	p = DecideCleanup(c, f, Cleanup{DeleteGone: true}, "")
	i := slices.IndexFunc(p.Remove, func(it Item) bool { return it.Branch == "squashed" })
	if i < 0 || !p.Remove[i].Force {
		t.Fatalf("delete_gone: squashed is not removed with force: %+v", p.Remove)
	}
	if slices.ContainsFunc(WithoutForce(p.Remove), func(it Item) bool { return it.Force }) {
		t.Error("WithoutForce kept a forced item")
	}
}

func TestDecideCleanupBulkAndOccupiedBranches(t *testing.T) {
	c, f := cleanupClone()
	c.DefaultSource = model.DefaultFromOriginHEAD
	p := DecideCleanup(c, f, Cleanup{DeleteGone: true, Bulk: true}, "")
	if slices.ContainsFunc(p.Remove, func(it Item) bool { return it.Force }) {
		t.Error("D deletes a branch that is not merged")
	}
	c.DefaultSource = model.DefaultFromGuess
	if p := DecideCleanup(c, f, Cleanup{Bulk: true}, ""); len(p.Remove) != 0 {
		t.Errorf("D removes from a clone whose default branch is guessed: %v", names(p.Remove))
	}
	if p := DecideCleanup(c, f, Cleanup{}, ""); len(p.Remove) == 0 {
		t.Error("d on one clone with a guessed default removes nothing")
	}
	// A rebase stopped in a worktree holds merged-branch though git lists
	// the worktree as detached: the branch is not d's to delete.
	c.Worktrees = append(c.Worktrees, model.Worktree{Path: "/wt/rebasing-merged", Occupies: "merged-branch", InProgress: "rebase"})
	if slices.Contains(names(DecideCleanup(c, f, Cleanup{}, "").Remove), "merged-branch") {
		t.Error("a branch held by a stopped rebase is removed")
	}
}

func TestStillRemovesLeavesWhatChanged(t *testing.T) {
	c, f := cleanupClone()
	confirmed := DecideCleanup(c, f, Cleanup{}, "")
	// After the confirm, an agent starts working in the landed worktree.
	c.Worktrees[1].Dirty = 3
	now := DecideCleanup(c, f, Cleanup{}, "")
	got := names(StillRemoves(confirmed, now))
	if slices.Contains(got, "landed") {
		t.Errorf("a worktree that got changes after the confirm is still removed: %v", got)
	}
	if !slices.Contains(got, "merged-branch") {
		t.Errorf("an unchanged item was dropped: %v", got)
	}
}

func TestJudge(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	made, touched := now.Add(-48*time.Hour), now.Add(-3*time.Hour)
	old := now.Add(-40 * 24 * time.Hour)
	c := model.Clone{Branches: []model.Branch{{Name: "done"}, {Name: "new"}, {Name: "dirty"}, {Name: "idle", Upstream: "origin/idle"},
		{Name: "unpushed", Upstream: "origin/unpushed", Ahead: 2}, {Name: "local", Upstream: ""}, {Name: "recent", Upstream: "origin/recent"}, {Name: "squashed", Upstream: "origin/squashed", Gone: true}}}
	f := model.CleanupFacts{Merged: map[string]bool{"done": true, "new": true}, WorkedOn: map[string]bool{"done": true, "dirty": true, "idle": true, "unpushed": true, "local": true, "recent": true}}
	w := func(branch string, edit func(*model.Worktree)) model.Worktree {
		x := model.Worktree{Branch: branch, Created: made, Active: touched}
		if edit != nil {
			edit(&x)
		}
		return x
	}
	for _, tc := range []struct {
		name  string
		w     model.Worktree
		want  Verdict
		since time.Time
	}{
		{"gone", w("x", func(x *model.Worktree) { x.Prunable = true }), VerdictGone, time.Time{}},
		{"merged, clean", w("done", nil), VerdictDone, touched},
		{"never committed to", w("new", nil), VerdictNew, made},
		{"changes", w("dirty", func(x *model.Worktree) { x.Dirty = 4; x.Active = old }), VerdictBusy, old},
		{"clean, long untouched", w("idle", func(x *model.Worktree) { x.Active = old }), VerdictIdle, old},
		{"unpushed, long untouched", w("unpushed", func(x *model.Worktree) { x.Active = old }), VerdictBusy, old},
		{"never pushed, long untouched", w("local", func(x *model.Worktree) { x.Active = old; x.AheadMain = 1 }), VerdictBusy, old},
		{"clean, touched lately", w("recent", nil), VerdictBusy, touched},
		{"merged but a merge in progress", w("done", func(x *model.Worktree) { x.InProgress = "merge" }), VerdictBusy, touched},
		{"detached, long untouched", w("", func(x *model.Worktree) { x.Active = old }), VerdictBusy, old},
		{"upstream deleted, own commits, long untouched", w("squashed", func(x *model.Worktree) { x.Active = old; x.AheadMain, x.Measured = 1, true }), VerdictBusy, old},
	} {
		v, since := Judge(c, tc.w, f, now)
		if v != tc.want || !since.Equal(tc.since) {
			t.Errorf("%s: %s since %v, want %s since %v", tc.name, v, since, tc.want, tc.since)
		}
	}
}

func TestAttention(t *testing.T) {
	quiet := model.Clone{OriginURL: "git@github.com:me/app.git", Default: "main", Status: model.Status{Head: "main", Upstream: "origin/main"}}
	if got := Attention(quiet, false); got != 0 {
		t.Errorf("a quiet clone scores %d", got)
	}
	if Attention(quiet, true) == 0 {
		t.Error("GitHub being ahead does not count")
	}
	noOrigin := model.Clone{Status: model.Status{Head: "main"}}
	unpushed := quiet
	unpushed.Ahead = 1
	if Attention(noOrigin, false) <= Attention(unpushed, false) || Attention(unpushed, false) <= Attention(quiet, true) {
		t.Error("work that exists nowhere else does not rank first")
	}
	neverPushed := quiet
	neverPushed.Head, neverPushed.Upstream = "feature", ""
	if Attention(neverPushed, false) < 50 {
		t.Error("a checked-out branch never pushed is not counted as unpushed work")
	}
}
