package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/babarot/gh-tidy/internal/gittest"
)

func TestScan(t *testing.T) {
	gittest.Isolate(t)
	base := t.TempDir()
	remote := gittest.Remote(t, base, "app")
	dir := filepath.Join(base, "src", "github.com", "me", "app")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Clone(t, remote, dir, "me/app")

	// Another clone moves main ahead by two on the remote.
	other := filepath.Join(base, "other")
	gittest.Git(t, base, "clone", "-q", remote, other)
	gittest.Commit(t, other, "a", "a")
	gittest.Commit(t, other, "b", "b")
	gittest.Git(t, other, "push", "-q", "origin", "main")
	gittest.Git(t, dir, "fetch", "-q")

	// A feature branch with one commit of its own, so it is behind main by
	// two and ahead of its upstream by one.
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature", "HEAD")
	gittest.Git(t, dir, "push", "-q", "-u", "origin", "feature")
	gittest.Commit(t, dir, "f", "feature work")

	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "branch", "local-only")
	wt := filepath.Join(base, "wt")
	gittest.Git(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wt)
	if err := os.WriteFile(filepath.Join(wt, "dirty"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Scan(context.Background(), dir)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"Slug", r.Slug, "me/app"},
		{"Head", r.Head, "feature"},
		{"Upstream", r.Upstream, "origin/feature"},
		{"Ahead", r.Ahead, 1},
		{"Behind", r.Behind, 0},
		{"Default", r.Default, "main"},
		{"DefaultSource", r.DefaultSource, DefaultFromOriginHEAD},
		{"BaseBehind", r.BaseBehind, 2},
		{"Unstaged", r.Unstaged, 1},
		{"Untracked", r.Untracked, 1},
		{"Worktrees", len(r.Worktrees), 2},
		{"linked worktree dirty", r.Worktrees[1].Dirty, 1},
		{"linked worktree measured", r.Worktrees[1].Measured, true},
		{"linked worktree ahead of main", r.Worktrees[1].Ahead, 1},
		{"linked worktree behind main", r.Worktrees[1].Behind, 2},
		{"NoUpstream", len(r.NoUpstream()), 2}, // local-only and wt-branch
		{"LastSubject", r.LastSubject, "feature work"},
		{"fetched", r.FetchedAt.IsZero(), false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if r.Quiet() || r.UntrackedOnly() {
		t.Errorf("a repository with unpushed work is not quiet")
	}
}

func TestSetDefaultFromGitHub(t *testing.T) {
	gittest.Isolate(t)
	base := t.TempDir()
	remote := gittest.Remote(t, base, "app")
	dir := filepath.Join(base, "app")
	gittest.Clone(t, remote, dir, "me/app")
	// GitHub's default moved to trunk; origin/HEAD still says main.
	gittest.Git(t, dir, "push", "-q", "origin", "main:trunk")
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature", "HEAD")
	other := filepath.Join(base, "other")
	gittest.Git(t, base, "clone", "-q", "-b", "trunk", remote, other)
	gittest.Commit(t, other, "t", "on trunk")
	gittest.Git(t, other, "push", "-q", "origin", "trunk")
	gittest.Git(t, dir, "fetch", "-q")

	r := Scan(context.Background(), dir)
	if r.Default != "main" || r.BaseBehind != 0 {
		t.Fatalf("before: default %q, base behind %d", r.Default, r.BaseBehind)
	}
	r.SetDefault(context.Background(), "trunk")
	if r.Default != "trunk" || r.DefaultSource != DefaultFromGitHub || r.BaseBehind != 1 {
		t.Errorf("after: default %q (%s), base behind %d; want trunk (github), 1", r.Default, r.DefaultSource, r.BaseBehind)
	}
	if r.IsStale(r.DefaultOid) {
		t.Errorf("a clone at GitHub's commit is not stale")
	}
	if !r.IsStale("0000000000000000000000000000000000000000") {
		t.Errorf("a clone at another commit is stale")
	}
}

func TestNoOrigin(t *testing.T) {
	gittest.Isolate(t)
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Commit(t, dir, "a", "a")
	r := Scan(context.Background(), dir)
	if r.HasOrigin() || r.Default != "" || r.Score() < 100 {
		t.Errorf("no origin: HasOrigin %v, Default %q, Score %d", r.HasOrigin(), r.Default, r.Score())
	}
}

func TestFind(t *testing.T) {
	gittest.Isolate(t)
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "a")
	if err := os.MkdirAll(filepath.Join(dir, "not-a-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Find(dir)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "a" {
		t.Errorf("Find = %v, %v", got, err)
	}
}
