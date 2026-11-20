package locate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/babarot/gh-tidy/internal/gittest"
)

func TestResolve(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "src")
	owner := filepath.Join(root, "github.com", "me")
	if err := os.MkdirAll(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(owner, "app")
	gittest.Clone(t, remote, app, "me/app")
	if err := os.MkdirAll(filepath.Join(app, "sub", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A clone whose directory name is not the repository's name.
	renamed := filepath.Join(owner, "old-name")
	gittest.Clone(t, remote, renamed, "me/new-name")
	// A worktree outside root.
	wt := filepath.Join(base, "worktrees", "app-wt")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "x", wt)
	// A clone with no origin, under root.
	local := filepath.Join(owner, "local")
	gittest.Git(t, owner, "init", "-q", local)
	// A second root, with a clone of its own.
	work := filepath.Join(base, "work")
	acme := filepath.Join(work, "github.com", "acme")
	if err := os.MkdirAll(acme, 0o755); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(acme, "api")
	gittest.Clone(t, remote, api, "acme/api")
	empty := filepath.Join(root, "github.com", "nobody")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

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
		{"owner dir of the second root", acme, Target{Kind: Owner, Host: "github.com", Owner: "acme"}, nil},
		{"clone in the second root", api, Target{Kind: Owner, Host: "github.com", Owner: "acme", Select: api}, nil},
		{"second root", work, Target{Kind: Picker, Host: "github.com"}, nil},
		{"outside", base, Target{}, ErrOutside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(ctx, tc.cwd, []string{root, work})
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Errorf("Resolve(%s) = %+v, %v; want %+v, %v", tc.cwd, got, err, tc.want, tc.err)
			}
		})
	}

}

func TestRoots(t *testing.T) {
	home, _ := os.UserHomeDir()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	src := filepath.Join(base, "src")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	got, err := Roots([]string{src, "~/code", link, src})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{src, filepath.Join(home, "code")}; !slices.Equal(got, want) {
		t.Errorf("Roots = %q; want %q, each once and resolved", got, want)
	}
	if _, err := Roots([]string{src, filepath.Join(src, "github.com", "me")}); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Errorf("a root inside another: %v", err)
	}
	if got, err := Roots(nil); err != nil || len(got) != 0 {
		t.Errorf("no roots: %q, %v", got, err)
	}
}

func TestCandidate(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(base, "src", "github.com", "Me", "app")
	renamed := filepath.Join(base, "src", "github.com", "you", "old-name")
	flat := filepath.Join(base, "code", "app")
	for _, d := range []string{app, renamed, flat} {
		if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Clone(t, remote, app, "me/app")
	gittest.Clone(t, remote, renamed, "you/new-name")
	wt := filepath.Join(base, "worktrees", "app-wt")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "x", wt)
	gittest.Clone(t, remote, flat, "me/app")
	ctx := context.Background()
	for _, tc := range []struct {
		name, cwd, want string
	}{
		{"in a clone, owner in another case", app, filepath.Join(base, "src")},
		{"renamed clone", renamed, filepath.Join(base, "src")},
		{"worktree outside", wt, filepath.Join(base, "src")},
		{"clone not laid out by host and owner", flat, ""},
		{"no clone", base, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := Candidate(ctx, tc.cwd); got != tc.want || ok != (tc.want != "") {
				t.Errorf("Candidate(%s) = %q, %v; want %q", tc.cwd, got, ok, tc.want)
			}
		})
	}
}
