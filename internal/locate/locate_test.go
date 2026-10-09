package locate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/babarot/gh-fleet/internal/gittest"
)

func TestResolve(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "src")
	owner := filepath.Join(root, "github.com", "me")
	os.MkdirAll(owner, 0o755)
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(owner, "app")
	gittest.Clone(t, remote, app, "me/app")
	os.MkdirAll(filepath.Join(app, "sub", "dir"), 0o755)
	// A clone whose directory name is not the repository's name.
	renamed := filepath.Join(owner, "old-name")
	gittest.Clone(t, remote, renamed, "me/new-name")
	// A worktree outside root.
	wt := filepath.Join(base, "worktrees", "app-wt")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "x", wt)
	// A clone with no origin, under root.
	local := filepath.Join(owner, "local")
	gittest.Git(t, owner, "init", "-q", local)
	empty := filepath.Join(root, "github.com", "nobody")
	os.MkdirAll(empty, 0o755)

	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cwd  string
		want Target
		err  error
	}{
		{"owner dir", owner, Target{Kind: Owner, Host: "github.com", Owner: "me"}, nil},
		{"empty owner dir", empty, Target{Kind: Owner, Host: "github.com", Owner: "nobody"}, nil},
		{"in a clone", filepath.Join(app, "sub", "dir"), Target{Kind: Owner, Host: "github.com", Owner: "me", Select: app}, nil},
		{"renamed clone", renamed, Target{Kind: Owner, Host: "github.com", Owner: "me", Select: renamed}, nil},
		{"worktree outside root", wt, Target{Kind: Owner, Host: "github.com", Owner: "me", Select: app}, nil},
		{"clone without origin", local, Target{Kind: Owner, Host: "github.com", Owner: "me", Select: local}, nil},
		{"host dir", filepath.Join(root, "github.com"), Target{Kind: Picker, Host: "github.com"}, nil},
		{"root", root, Target{Kind: Picker, Host: "github.com"}, nil},
		{"outside", base, Target{}, ErrOutside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(ctx, tc.cwd, root)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Errorf("Resolve(%s) = %+v, %v; want %+v, %v", tc.cwd, got, err, tc.want, tc.err)
			}
		})
	}

	owners, err := Owners(root, "github.com")
	if err != nil || len(owners) != 2 || owners[0] != (OwnerDir{"me", 3}) || owners[1] != (OwnerDir{"nobody", 0}) {
		t.Errorf("Owners = %+v, %v", owners, err)
	}
}

func TestRoot(t *testing.T) {
	gittest.Isolate(t)
	home, _ := os.UserHomeDir()
	if got := Root(context.Background(), "~/code"); got != filepath.Join(home, "code") {
		t.Errorf("Root(~/code) = %s", got)
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(cfg, []byte("[ghq]\n\troot = ~/first\n\troot = ~/second\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	if got := Root(context.Background(), ""); got != filepath.Join(home, "first") {
		t.Errorf("Root from ghq.root = %s", got)
	}
}
