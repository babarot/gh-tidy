package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/github"
)

// A worktree reached through a link is where git says it is, so that d
// sees gh tidy runs in it.
func TestResolvedFollowsLinks(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	real := filepath.Join(base, "real", "wt")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if got := resolved(filepath.Join(base, "link", "wt")); got != real {
		t.Errorf("resolved = %s, want %s", got, real)
	}
	if got := resolved("/no/such/dir"); got != "/no/such/dir" {
		t.Errorf("an unresolvable path = %s", got)
	}
}

func TestOwners(t *testing.T) {
	root := t.TempDir()
	for _, o := range []string{"zed", "Acme", "me"} {
		if err := os.MkdirAll(filepath.Join(root, "github.com", o, "x", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := owners([]string{root}, "github.com", github.Viewer{Login: "me", Orgs: []string{"acme", "corp"}},
		config.Owners{Add: []string{"github.com/extra"}, Hide: []string{"zed"}})
	want := []string{"acme", "corp", "extra", "me"}
	if len(got) != len(want) {
		t.Fatalf("owners = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("owners = %v, want %v", got, want)
		}
	}
}

// An owner on another host is refused, not read on github.com under the
// same name.
func TestAnotherHostIsRefused(t *testing.T) {
	var out strings.Builder
	err := Run("gh tidy", []string{"ghe.example.com/acme"}, &out)
	if err == nil || !strings.Contains(err.Error(), "github.com only") {
		t.Errorf("Run(ghe.example.com/acme) = %v", err)
	}
}
