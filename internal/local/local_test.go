package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/babarot/gh-tidy/internal/gittest"
	"github.com/babarot/gh-tidy/internal/gitx"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
)

// world is a bare remote and a clone of it, with a pusher clone that moves
// the remote's main ahead as someone else would.
type world struct {
	t                   *testing.T
	base, remote, clone string
	pusher              string
	pushed              int // commits pushed so far, so each has its own content
}

func newWorld(t *testing.T) *world {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{t: t, base: base}
	w.remote = gittest.Remote(t, base, "app")
	w.clone = filepath.Join(base, "src", "github.com", "me", "app")
	if err := os.MkdirAll(filepath.Dir(w.clone), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Clone(t, w.remote, w.clone, "me/app")
	w.pusher = filepath.Join(base, "pusher")
	gittest.Git(t, base, "clone", "-q", w.remote, w.pusher)
	return w
}

// push moves the remote's main ahead by n commits.
func (w *world) push(n int) {
	gittest.Git(w.t, w.pusher, "pull", "-q")
	for range n {
		w.pushed++
		gittest.Commit(w.t, w.pusher, "p", fmt.Sprintf("pushed %d", w.pushed))
	}
	gittest.Git(w.t, w.pusher, "push", "-q", "origin", "main")
}

func (w *world) oid(dir, ref string) string { return gittest.Git(w.t, dir, "rev-parse", ref) }

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	w := newWorld(t)
	dir := w.clone
	w.push(2)
	gittest.Git(t, dir, "fetch", "-q")
	// A feature branch with one commit of its own: behind main by two,
	// ahead of its upstream by one.
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature", "HEAD")
	gittest.Git(t, dir, "push", "-q", "-u", "origin", "feature")
	gittest.Commit(t, dir, "f", "feature work")
	write(t, filepath.Join(dir, "README"), "changed\n")
	write(t, filepath.Join(dir, "new.txt"), "x\n")
	gittest.Git(t, dir, "branch", "local-only")
	wt := filepath.Join(w.base, "wt")
	gittest.Git(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	write(t, filepath.Join(wt, "dirty"), "x\n")

	c := Scan(context.Background(), dir)
	if c.Err != "" {
		t.Fatal(c.Err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"Key", c.Key, model.NewCloneKey(dir)},
		{"Remote", c.Remote, model.RemoteKey("github.com/me/app")},
		{"Head", c.Head, "feature"},
		{"Upstream", c.Upstream, "origin/feature"},
		{"Ahead", c.Ahead, 1},
		{"Default", c.Default, "main"},
		{"DefaultSource", c.DefaultSource, model.DefaultFromOriginHEAD},
		{"BaseBehind", c.BaseBehind, 2},
		{"Unstaged", c.Unstaged, 1},
		{"Untracked", c.Untracked, 1},
		{"Worktrees", len(c.Worktrees), 2},
		{"main worktree dirty", c.Worktrees[0].Dirty, 2},
		{"linked worktree dirty", c.Worktrees[1].Dirty, 1},
		{"linked worktree ahead of main", c.Worktrees[1].AheadMain, 1},
		{"linked worktree behind main", c.Worktrees[1].BehindMain, 2},
		{"linked worktree made", c.Worktrees[1].Created.IsZero(), false},
		{"LastSubject", c.LastSubject, "feature work"},
		{"fetched", c.FetchedAt.IsZero(), false},
	}
	for _, x := range checks {
		if x.got != x.want {
			t.Errorf("%s = %v, want %v", x.name, x.got, x.want)
		}
	}
}

func TestWorktreeTimesAndOperationsInProgress(t *testing.T) {
	w := newWorld(t)
	wt := filepath.Join(w.base, "wt")
	gittest.Git(t, w.clone, "worktree", "add", "-q", "-b", "topic", wt)
	before := Scan(context.Background(), w.clone).Worktrees[1]
	if before.Created.IsZero() || before.Created.After(before.Active) {
		t.Fatalf("made %v, last touched %v", before.Created, before.Active)
	}

	// A merge that stops on a conflict leaves MERGE_HEAD in the worktree's
	// own git directory.
	write(t, filepath.Join(wt, "README"), "topic\n")
	gittest.Git(t, wt, "commit", "-q", "-am", "topic")
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(w.clone, "README"), "other\n")
	gittest.Git(t, w.clone, "commit", "-q", "-am", "other")
	if err := gittestTry(wt, "merge", "-q", "other"); err == nil {
		t.Fatal("the merge did not stop on a conflict")
	}
	c := Scan(context.Background(), w.clone)
	if got := c.Worktrees[1].InProgress; got != "merge" {
		t.Errorf("linked worktree in progress = %q, want merge", got)
	}
	if got := c.Worktrees[0].InProgress; got != "" {
		t.Errorf("main worktree in progress = %q, want none", got)
	}
}

// gitRun runs git as the code under test does, returning its error.
func gitRun(dir string, args ...string) (string, error) {
	return gitx.Run(context.Background(), dir, args...)
}

// gittestTry runs git and returns its error instead of failing the test.
func gittestTry(dir string, args ...string) error {
	_, err := gitRun(dir, args...)
	return err
}

func TestNoOriginAndFind(t *testing.T) {
	gittest.Isolate(t)
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main", "a")
	gittest.Commit(t, filepath.Join(dir, "a"), "x", "x")
	if err := os.MkdirAll(filepath.Join(dir, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Find(dir)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "a" {
		t.Fatalf("Find = %v, %v", got, err)
	}
	c := Scan(context.Background(), got[0])
	if c.HasOrigin() || c.Default != "" || c.Remote != "" {
		t.Errorf("no origin: origin %q, default %q, remote %q", c.OriginURL, c.Default, c.Remote)
	}
}

func TestSetDefaultFromGitHub(t *testing.T) {
	w := newWorld(t)
	// GitHub's default moved to trunk; origin/HEAD still says main.
	gittest.Git(t, w.clone, "push", "-q", "origin", "main:trunk")
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature", "HEAD")
	gittest.Git(t, w.pusher, "fetch", "-q")
	gittest.Git(t, w.pusher, "checkout", "-q", "-b", "trunk", "origin/trunk")
	gittest.Commit(t, w.pusher, "t", "on trunk")
	gittest.Git(t, w.pusher, "push", "-q", "origin", "trunk")
	gittest.Git(t, w.clone, "fetch", "-q")

	c := Scan(context.Background(), w.clone)
	if c.Default != "main" || c.BaseBehind != 0 {
		t.Fatalf("before: default %q, base behind %d", c.Default, c.BaseBehind)
	}
	SetDefault(context.Background(), &c, "trunk")
	if c.Default != "trunk" || c.DefaultSource != model.DefaultFromGitHub || c.BaseBehind != 1 {
		t.Errorf("after: default %q (%s), base behind %d; want trunk (github), 1", c.Default, c.DefaultSource, c.BaseBehind)
	}
}

// advance runs what p does to one clone: fetch, read the facts, decide,
// and move the default branch.
func advance(t *testing.T, dir string) policy.Advance {
	t.Helper()
	ctx := context.Background()
	if err := Fetch(ctx, dir); err != nil {
		t.Fatal(err)
	}
	c := Scan(ctx, dir)
	d := policy.DecideAdvance(AdvanceFacts(ctx, dir, c.Default, c.DefaultSource))
	switch d.Action {
	case policy.AdvanceRef:
		if err := FastForwardRef(ctx, dir, c.Default); err != nil {
			t.Fatal(err)
		}
	case policy.AdvanceMerge:
		if err := MergeFastForward(ctx, d.Worktree, c.Default); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestAdvanceWhenMainIsCheckedOutNowhere(t *testing.T) {
	w := newWorld(t)
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
	gittest.Commit(t, w.clone, "f", "feature work")
	feature := w.oid(w.clone, "feature")
	w.push(2)
	d := advance(t, w.clone)
	if d.Action != policy.AdvanceRef || d.Commits != 2 {
		t.Fatalf("decision = %+v", d)
	}
	if w.oid(w.clone, "main") != w.oid(w.clone, "origin/main") {
		t.Error("main did not move to origin/main")
	}
	if w.oid(w.clone, "feature") != feature || gittest.Git(t, w.clone, "branch", "--show-current") != "feature" {
		t.Error("the branch being worked on was touched")
	}
}

func TestAdvanceInTheWorktreeHoldingMain(t *testing.T) {
	w := newWorld(t)
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
	wt := filepath.Join(w.base, "main-wt")
	gittest.Git(t, w.clone, "worktree", "add", "-q", wt, "main")
	w.push(1)
	d := advance(t, w.clone)
	if d.Action != policy.AdvanceMerge || d.Worktree != wt {
		t.Fatalf("decision = %+v", d)
	}
	if w.oid(wt, "HEAD") != w.oid(w.clone, "origin/main") {
		t.Error("the worktree holding main did not move")
	}
}

func TestAdvanceSkips(t *testing.T) {
	t.Run("changes where main is checked out", func(t *testing.T) {
		w := newWorld(t)
		write(t, filepath.Join(w.clone, "README"), "edit\n")
		before := w.oid(w.clone, "main")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip {
			t.Fatalf("decision = %+v", d)
		}
		if w.oid(w.clone, "main") != before {
			t.Error("main moved under uncommitted changes")
		}
	})
	t.Run("main has commits origin does not", func(t *testing.T) {
		w := newWorld(t)
		gittest.Commit(t, w.clone, "l", "local only")
		gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip {
			t.Fatalf("decision = %+v", d)
		}
		// git itself refuses too: the refspec has no +.
		if err := FastForwardRef(context.Background(), w.clone, "main"); err == nil {
			t.Error("a non-fast-forward was made")
		}
	})
	t.Run("main tracks another remote", func(t *testing.T) {
		w := newWorld(t)
		gittest.Git(t, w.clone, "remote", "add", "upstream", w.remote)
		gittest.Git(t, w.clone, "fetch", "-q", "upstream")
		gittest.Git(t, w.clone, "branch", "-q", "--set-upstream-to", "upstream/main", "main")
		gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("no local main", func(t *testing.T) {
		w := newWorld(t)
		gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
		gittest.Git(t, w.clone, "branch", "-q", "-D", "main")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip {
			t.Fatalf("decision = %+v", d)
		}
		if _, err := gitRun(w.clone, "rev-parse", "-q", "--verify", "refs/heads/main"); err == nil {
			t.Error("a main branch was created")
		}
	})
}

func TestAdvanceSkipsWhatHoldsMainUnseen(t *testing.T) {
	t.Run("a rebase stopped on main", func(t *testing.T) {
		w := newWorld(t)
		w.push(1)
		gittest.Git(t, w.clone, "pull", "-q")
		w.push(1)
		// git lists the worktree as detached while the rebase waits.
		t.Setenv("GIT_SEQUENCE_EDITOR", "true")
		if err := gittestTry(w.clone, "rebase", "-q", "--exec", "false", "HEAD~1"); err == nil {
			t.Fatal("the rebase did not stop")
		}
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip || !strings.Contains(d.Why, "rebase is in progress") {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("a bisect started on main", func(t *testing.T) {
		w := newWorld(t)
		w.push(2)
		gittest.Git(t, w.clone, "pull", "-q")
		gittest.Git(t, w.clone, "bisect", "start")
		gittest.Git(t, w.clone, "bisect", "bad", "HEAD")
		gittest.Git(t, w.clone, "bisect", "good", "HEAD~2")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip || !strings.Contains(d.Why, "bisect") {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("main checked out in two worktrees", func(t *testing.T) {
		w := newWorld(t)
		gittest.Git(t, w.clone, "worktree", "add", "-q", "--force", filepath.Join(w.base, "second"), "main")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip || !strings.Contains(d.Why, "2 worktrees") {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("main held by a worktree that was moved", func(t *testing.T) {
		w := newWorld(t)
		gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
		wt := filepath.Join(w.base, "main-wt")
		gittest.Git(t, w.clone, "worktree", "add", "-q", wt, "main")
		if err := os.Rename(wt, wt+"-moved"); err != nil {
			t.Fatal(err)
		}
		before := w.oid(w.clone, "main")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip || !strings.Contains(d.Why, "cannot find") {
			t.Fatalf("decision = %+v", d)
		}
		if w.oid(w.clone, "main") != before {
			t.Error("main moved under a worktree that may be in use")
		}
	})
	t.Run("main held by a rebase in a worktree that was moved", func(t *testing.T) {
		w := newWorld(t)
		w.push(1)
		gittest.Git(t, w.clone, "pull", "-q")
		gittest.Git(t, w.clone, "checkout", "-q", "-b", "feature")
		wt := filepath.Join(w.base, "main-wt")
		gittest.Git(t, w.clone, "worktree", "add", "-q", wt, "main")
		t.Setenv("GIT_SEQUENCE_EDITOR", "true")
		if err := gittestTry(wt, "rebase", "-q", "--exec", "false", "HEAD~1"); err == nil {
			t.Fatal("the rebase did not stop")
		}
		if err := os.Rename(wt, wt+"-moved"); err != nil {
			t.Fatal(err)
		}
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceSkip || !strings.Contains(d.Why, "cannot find") {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("untracked files are not in the way", func(t *testing.T) {
		w := newWorld(t)
		write(t, filepath.Join(w.clone, "notes.txt"), "mine\n")
		w.push(1)
		if d := advance(t, w.clone); d.Action != policy.AdvanceMerge {
			t.Fatalf("decision = %+v", d)
		}
		if got, _ := os.ReadFile(filepath.Join(w.clone, "notes.txt")); string(got) != "mine\n" {
			t.Error("the untracked file was touched")
		}
	})
}

func TestScanSeesTheBranchABisectHolds(t *testing.T) {
	w := newWorld(t)
	w.push(2)
	gittest.Git(t, w.clone, "pull", "-q")
	gittest.Git(t, w.clone, "bisect", "start")
	gittest.Git(t, w.clone, "bisect", "bad", "HEAD")
	gittest.Git(t, w.clone, "bisect", "good", "HEAD~2")
	m := Scan(context.Background(), w.clone).Worktrees[0]
	if m.Branch != "" || m.Holds() != "main" || m.InProgress != "bisect" {
		t.Errorf("main worktree: branch %q, holds %q, in progress %q", m.Branch, m.Holds(), m.InProgress)
	}
}

func TestCleanupFacts(t *testing.T) {
	w := newWorld(t)
	land := func(name string) string {
		wt := filepath.Join(w.base, "wt", name)
		gittest.Git(t, w.clone, "fetch", "-q")
		gittest.Git(t, w.clone, "worktree", "add", "-q", "-b", name, wt, "origin/main")
		gittest.Commit(t, wt, name+".txt", name)
		gittest.Git(t, wt, "push", "-q", "origin", name+":main")
		return wt
	}
	landed := land("landed")
	land("landed-dirty")
	write(t, filepath.Join(w.base, "wt", "landed-dirty", "x"), "x")
	gittest.Git(t, w.clone, "worktree", "add", "-q", "-b", "fresh", filepath.Join(w.base, "wt", "fresh"))
	gittest.Git(t, w.clone, "fetch", "-q")
	gittest.Git(t, w.clone, "branch", "merged-branch", "origin/main")

	ctx := context.Background()
	c := Scan(ctx, w.clone)
	f, err := CleanupFacts(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if f.Base != "origin/main" || !f.Merged["landed"] || !f.Merged["merged-branch"] || f.WorkedOn["fresh"] || !f.WorkedOn["landed"] {
		t.Fatalf("facts = %+v", f)
	}
	p := policy.DecideCleanup(c, f, policy.Cleanup{}, "")
	var removed []string
	for _, it := range p.Remove {
		removed = append(removed, it.Branch)
	}
	slices.Sort(removed)
	if want := []string{"landed", "merged-branch"}; !slices.Equal(removed, want) {
		t.Fatalf("remove = %v, want %v", removed, want)
	}

	// Right before removing, the worktree is read again; then its branch is
	// deleted only if still merged.
	if n, err := WorktreeDirty(ctx, landed); err != nil || n != 0 {
		t.Fatalf("landed: %d changes, %v", n, err)
	}
	gittest.Git(t, w.clone, "worktree", "remove", landed)
	if err := DeleteMergedBranch(ctx, w.clone, "landed", f.Base); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "unmerged")
	gittest.Commit(t, w.clone, "u", "not merged")
	gittest.Git(t, w.clone, "checkout", "-q", "main")
	if err := DeleteMergedBranch(ctx, w.clone, "unmerged", f.Base); err == nil {
		t.Error("an unmerged branch was deleted")
	}
}

func TestCleanupFactsWithoutDefault(t *testing.T) {
	if _, err := CleanupFacts(context.Background(), model.Clone{Path: t.TempDir()}); !errors.Is(err, ErrNoDefault) {
		t.Errorf("err = %v, want ErrNoDefault", err)
	}
}
