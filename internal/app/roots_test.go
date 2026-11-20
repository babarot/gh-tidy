package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/babarot/gh-tidy/internal/gittest"
)

// With no roots set, gh tidy offers the root of the clone it starts in
// and writes it only on a yes, to a file that does not exist yet; else it
// stops with the line to write.
func TestNoRootsSet(t *testing.T) {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	remote := gittest.Remote(t, base, "app")
	app := filepath.Join(base, "src", "github.com", "me", "app")
	if err := os.MkdirAll(filepath.Dir(app), 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Clone(t, remote, app, "me/app")
	src := filepath.Join(base, "src")
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		cwd      string
		answer   string
		terminal bool
		file     string // the config file there before; empty for none
		want     string // the error, or empty when it starts
	}{
		{"yes", app, "y\n", true, "", ""},
		{"enter alone", app, "\n", true, "", "add to"},
		{"no", app, "n\n", true, "", "add to"},
		{"no answer", app, "", true, "", "add to"},
		{"not a terminal", app, "y\n", false, "", "add to"},
		{"a file of the user's", app, "y\n", true, "[tui]\ntheme = \"nord\"\n", "add to"},
		{"no clone", base, "y\n", true, "", "write where your clones live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gh-tidy", "config.toml")
			if tc.file != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var out strings.Builder
			got, err := roots(ctx, nil, path, tc.cwd, ask{in: strings.NewReader(tc.answer), out: &out, terminal: tc.terminal})
			data, _ := os.ReadFile(path)
			switch {
			case tc.want == "":
				if err != nil || len(got) != 1 || got[0] != src {
					t.Fatalf("roots = %q, %v; want %s", got, err, src)
				}
				if !strings.Contains(string(data), "[core]\nroots = [") || !strings.Contains(out.String(), "[y/N]") {
					t.Errorf("file %q, asked %q", data, out.String())
				}
			case err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "roots = ["):
				t.Errorf("err = %v; want one with %q and the line to write", err, tc.want)
			case string(data) != tc.file:
				t.Errorf("the config file was written: %q", data)
			}
			if !tc.terminal && out.Len() > 0 {
				t.Errorf("asked off a terminal: %q", out.String())
			}
		})
	}
	// Roots set are used as they are, resolved.
	if got, err := roots(ctx, []string{src}, "", base, ask{}); err != nil || len(got) != 1 || got[0] != src {
		t.Errorf("roots set = %q, %v", got, err)
	}
}
