package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/gittest"
	"github.com/babarot/gh-fleet/internal/repo"
)

// land commits on a new branch in a worktree and fast-forwards main onto
// it on the remote, as git land does.
func land(t *testing.T, app, wt, branch string) {
	gittest.Git(t, app, "fetch", "-q")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", branch, wt, "origin/main")
	gittest.Commit(t, wt, branch+".txt", branch)
	gittest.Git(t, wt, "push", "-q", "origin", branch+":main")
}

func names(items []Item) []string {
	var out []string
	for _, it := range items {
		n := it.Branch
		if it.Kind == Prunable {
			n = "prunable:" + filepath.Base(it.Path)
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func TestDecideAndRun(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(base, "app")
	gittest.Clone(t, remote, app, "")
	wts := filepath.Join(base, "wt")

	land(t, app, filepath.Join(wts, "landed"), "landed")
	land(t, app, filepath.Join(wts, "landed-dirty"), "landed-dirty")
	os.WriteFile(filepath.Join(wts, "landed-dirty", "x"), []byte("x"), 0o644)
	land(t, app, filepath.Join(wts, "here"), "here")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "fresh", filepath.Join(wts, "fresh"))
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "working", filepath.Join(wts, "working"))
	gittest.Commit(t, filepath.Join(wts, "working"), "w", "not landed")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "vanished", filepath.Join(wts, "vanished"))
	os.RemoveAll(filepath.Join(wts, "vanished"))
	gittest.Git(t, app, "fetch", "-q")

	gittest.Git(t, app, "branch", "merged-branch", "origin/main")
	gittest.Git(t, app, "branch", "keep-me", "origin/main")
	// A squash-merged branch: pushed, its own commit, upstream deleted.
	gittest.Git(t, app, "checkout", "-q", "-b", "squashed")
	gittest.Commit(t, app, "s", "squashed work")
	gittest.Git(t, app, "push", "-q", "-u", "origin", "squashed")
	gittest.Git(t, app, "push", "-q", "origin", "--delete", "squashed")
	gittest.Git(t, app, "checkout", "-q", "main")
	gittest.Git(t, app, "fetch", "-q", "--prune")

	ctx := context.Background()
	r := repo.Scan(ctx, app)
	cfg := config.Default().Cleanup
	cfg.Protect = []string{"keep-me"}
	p, err := Decide(ctx, r, cfg, filepath.Join(wts, "here", "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(p.Remove), []string{"landed", "merged-branch", "prunable:vanished"}; !slices.Equal(got, want) {
		t.Errorf("remove = %v, want %v", got, want)
	}
	if got, want := names(p.Keep), []string{"fresh", "here", "landed-dirty", "squashed"}; !slices.Equal(got, want) {
		t.Errorf("keep = %v, want %v", got, want)
	}
	for _, k := range p.Keep {
		if k.Branch == "fresh" && !strings.Contains(k.Why, "no commits") {
			t.Errorf("fresh kept for %q", k.Why)
		}
	}

	n, err := Run(ctx, r, p, cfg, map[string]string{})
	if err != nil || n != 3 {
		t.Fatalf("Run = %d, %v", n, err)
	}
	after := repo.Scan(ctx, app)
	var branches []string
	for _, b := range after.Branches {
		branches = append(branches, b.Name)
	}
	slices.Sort(branches)
	if want := []string{"fresh", "here", "keep-me", "landed-dirty", "main", "squashed", "vanished", "working"}; !slices.Equal(branches, want) {
		t.Errorf("branches after = %v, want %v", branches, want)
	}
	if _, err := os.Stat(filepath.Join(wts, "landed")); !os.IsNotExist(err) {
		t.Errorf("the landed worktree is still there")
	}
	if len(after.Worktrees) != 5 { // main, landed-dirty, here, fresh, working
		var paths []string
		for _, w := range after.Worktrees {
			paths = append(paths, w.Path)
		}
		t.Errorf("worktrees after = %v", paths)
	}
}

func TestDeleteGone(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(base, "app")
	gittest.Clone(t, remote, app, "")
	gittest.Git(t, app, "checkout", "-q", "-b", "squashed")
	gittest.Commit(t, app, "s", "squashed work")
	gittest.Git(t, app, "push", "-q", "-u", "origin", "squashed")
	gittest.Git(t, app, "push", "-q", "origin", "--delete", "squashed")
	gittest.Git(t, app, "checkout", "-q", "main")
	gittest.Git(t, app, "fetch", "-q", "--prune")

	ctx := context.Background()
	cfg := config.Default().Cleanup
	cfg.DeleteGone = true
	r := repo.Scan(ctx, app)
	p, err := Decide(ctx, r, cfg, "")
	if err != nil || len(p.Remove) != 1 || !p.Remove[0].Force {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if _, err := Run(ctx, r, p, cfg, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWorktreeRemoveCommand(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(base, "app")
	gittest.Clone(t, remote, app, "")
	wt := filepath.Join(base, "wt", "landed")
	land(t, app, wt, "landed")
	gittest.Git(t, app, "fetch", "-q")
	log := filepath.Join(base, "removed.log")

	ctx := context.Background()
	cfg := config.Default().Cleanup
	// A tool's own command: record the call, then remove it as git would.
	cfg.WorktreeRemove = []string{"sh", "-c", `echo "$1 $2" >> ` + log + ` && git worktree remove "$1"`, "sh", "{path}", "{name}"}
	r := repo.Scan(ctx, app)
	p, err := Decide(ctx, r, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, r, p, cfg, map[string]string{"name": "app"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	if strings.TrimSpace(string(got)) != wt+" app" {
		t.Errorf("command got %q", got)
	}
}

func TestNoDefault(t *testing.T) {
	r := &repo.Repo{Path: t.TempDir()}
	if _, err := Decide(context.Background(), r, config.Default().Cleanup, ""); err != ErrNoDefault {
		t.Errorf("Decide = %v, want ErrNoDefault", err)
	}
}
