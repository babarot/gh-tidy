// Package recording is the made-up world the demo recording runs gh tidy
// against: real git clones and remotes in a temporary directory, and a
// made-up GitHub that answers from them. Every person, repository and
// package in it is made up. It is never part of gh tidy (ADR-010).
package recording

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/gitx"
)

// Owner is the made-up owner whose clones the recording shows.
const Owner = "mika"

// World is the made-up world, built under one directory.
type World struct {
	// Home is the directory the world is under, resolved, also given as
	// the home directory so that paths read ~/src/...
	Home string
	// Root is where the clones are, <Home>/src.
	Root string
	// Config is the config file the recording reads.
	Config string
	// Built is when the world was built; the made-up times are from it.
	Built time.Time

	repos   []repo
	remotes string // the bare remotes and the repositories they are made from
}

// repo is one repository of the world, as GitHub tells of it.
type repo struct {
	name    string
	cloned  bool
	stars   int
	pushed  time.Duration // ago
	ci      string
	alerts  []alert
	issues  []opened
	prs     []opened
	head    string // the bare remote's main, read once built
	private bool
	id      int64 // GitHub's number for it, set once built
	behind  int   // commits GitHub has that the clone has not fetched
	dirty   bool  // a change not committed in the clone
}

type alert struct{ severity, pkg, ecosystem, vulnerable, fixed, manifest string }

type opened struct {
	number int
	title  string
	author string
	made   time.Duration // ago; less than baseline is new
}

// baseline is how long before the world was built its owner was first
// read: what is made after it is new.
const baseline = 24 * time.Hour

// repos are the repositories of the world.
func repos() []repo {
	h := time.Hour
	return []repo{
		{name: "agent-runner", cloned: true, stars: 128, pushed: 2*h + 30*time.Minute, ci: "SUCCESS", behind: 3,
			issues: []opened{{41, "Retry the runner when the socket closes", "kai", 3*h + 10*time.Minute}, {37, "Document the cache layout", "ren", 52 * h}},
			prs:    []opened{{40, "Speed up status parsing", "hana", 75 * h}}},
		{name: "calendar-sync", cloned: true, stars: 41, pushed: 26 * h, ci: "FAILURE", behind: 2,
			alerts: []alert{{"critical", "ical-parser", "npm", "< 2.4.1", "2.4.1", "package-lock.json"}, {"moderate", "tmp-dir", "npm", "<= 0.9.0", "", "package-lock.json"}},
			issues: []opened{{12, "Events after midnight land on the wrong day", "ren", 98 * h}}},
		{name: "dotfiles", cloned: true, stars: 16, pushed: 5*h + 20*time.Minute, ci: "SUCCESS", private: true, dirty: true},
		{name: "fern-docs", stars: 57, pushed: 50 * h, ci: "SUCCESS",
			alerts: []alert{{"high", "markdown-it-lite", "npm", "< 13.0.2", "13.0.2", "package-lock.json"}, {"moderate", "glob-walk", "npm", "< 4.1.0", "4.1.0", "package-lock.json"}, {"low", "ansi-tint", "npm", "< 2.0.1", "2.0.1", "package-lock.json"}}},
		{name: "harbor-proxy", cloned: true, stars: 88, pushed: 9*h + 40*time.Minute, ci: "SUCCESS", behind: 1, dirty: true,
			prs: []opened{{21, "Pass the client's address on", "kai", 130 * h}}},
		{name: "kata-notes", cloned: true, stars: 12, pushed: 410 * h, ci: "SUCCESS"},
		{name: "ledger-cli", cloned: true, stars: 64, pushed: 14*h + 15*time.Minute, ci: "SUCCESS", behind: 3,
			alerts: []alert{{"low", "csv-quick", "go", "< 0.8.2", "0.8.2", "go.mod"}},
			issues: []opened{{9, "Import OFX files", "ren", 160 * h}, {7, "Round to the cent", "hana", 300 * h}}},
		{name: "mdlint-action", cloned: true, stars: 31, pushed: 70 * h, ci: "FAILURE"},
		{name: "orbit-sim", stars: 19, pushed: 260 * h, ci: "SUCCESS"},
		{name: "paper-cranes", cloned: true, stars: 7, pushed: 520 * h, ci: "SUCCESS"},
		{name: "pixel-garden", cloned: true, stars: 9, pushed: 20*h + 10*time.Minute, ci: "SUCCESS"},
		{name: "quill-md", cloned: true, stars: 45, pushed: 7*h + 50*time.Minute, ci: "SUCCESS", behind: 4},
		{name: "river-bench", cloned: true, stars: 22, pushed: 180 * h, ci: "SUCCESS"},
		{name: "sakura-theme", cloned: true, stars: 214, pushed: 90 * h, ci: "SUCCESS"},
		{name: "sundial", stars: 33, pushed: 6 * h, ci: "SUCCESS",
			prs: []opened{{5, "Show the hour hand in the dark theme", "kai", 5*h + 30*time.Minute}}},
		{name: "tiny-kv", cloned: true, stars: 73, pushed: 40 * h, ci: "SUCCESS", dirty: true},
		{name: "tool-shed", stars: 23, pushed: 100 * h, ci: "SUCCESS",
			alerts: []alert{{"high", "yaml-lite", "go", "< 1.3.0", "1.3.0", "go.mod"}},
			issues: []opened{{3, "Explain the shelf layout", "kai", 120 * h}}},
		{name: "trail-notes", cloned: true, stars: 37, pushed: 30 * h, ci: "PENDING", behind: 2,
			prs: []opened{{8, "Add a map of the trail", "hana", 54 * h}}},
		{name: "weather-cli", cloned: true, stars: 4, pushed: 230 * h, ci: "SUCCESS"},
	}
}

// stamp is the file that says when the world was built.
const stamp = "world.json"

// Build makes the world under dir. The caller has set the environment so
// that git reads none of the user's settings (see the demo's main).
func Build(ctx context.Context, dir string) (*World, error) {
	w, err := newWorld(dir, time.Now())
	if err != nil {
		return nil, err
	}
	for i := range w.repos {
		if err := w.make(ctx, &w.repos[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", w.repos[i].name, err)
		}
	}
	// The commits of the world are made; what gh tidy makes from now on
	// is made at the time it is made.
	os.Unsetenv("GIT_AUTHOR_DATE")
	os.Unsetenv("GIT_COMMITTER_DATE")
	cfg := fmt.Sprintf("[core]\nroots = [%q]\n\n[tui]\ntheme = \"catppuccin-mocha\"\n", w.Root)
	if err := os.WriteFile(w.Config, []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(map[string]time.Time{"built": w.Built})
	if err := os.WriteFile(filepath.Join(w.Home, stamp), data, 0o644); err != nil {
		return nil, err
	}
	return w, nil
}

// Open reads back a world Build made under dir, so that the recording
// starts at once: building it takes a few seconds of git.
func Open(ctx context.Context, dir string) (*World, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	home, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(home, stamp))
	if err != nil {
		return nil, fmt.Errorf("no world built under %s: %w", dir, err)
	}
	var st map[string]time.Time
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	w, err := newWorld(home, st["built"])
	if err != nil {
		return nil, err
	}
	for i := range w.repos {
		head, err := gitx.Run(ctx, filepath.Join(w.remotes, w.repos[i].name+".git"), "rev-parse", "refs/heads/main")
		if err != nil {
			return nil, err
		}
		w.repos[i].head = strings.TrimSpace(head)
	}
	return w, nil
}

func newWorld(dir string, built time.Time) (*World, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	home, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	w := &World{Home: home, Root: filepath.Join(home, "src"), Config: filepath.Join(home, "config.toml"), Built: built,
		remotes: filepath.Join(home, "remotes"), repos: repos()}
	for i := range w.repos {
		w.repos[i].id = int64(1001 + i)
	}
	return w, nil
}

// at sets the time the next commits are made at, d before the world was
// built.
func (w *World) at(d time.Duration) {
	t := w.Built.Add(-d).Format(time.RFC3339)
	os.Setenv("GIT_AUTHOR_DATE", t)
	os.Setenv("GIT_COMMITTER_DATE", t)
}

// clone clones the bare remote of the repository name into dir, its
// origin GitHub's name for it, sent to the bare remote by the clone's own
// insteadOf.
func (w *World) clone(ctx context.Context, name, dir string) error {
	bare := filepath.Join(w.remotes, name+".git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := git(ctx, filepath.Dir(dir), "clone", "-q", bare, dir); err != nil {
		return err
	}
	url := "git@github.com:" + Owner + "/" + name + ".git"
	for _, args := range [][]string{{"remote", "set-url", "origin", url}, {"config", "url." + bare + ".insteadOf", url}} {
		if err := git(ctx, dir, args...); err != nil {
			return err
		}
	}
	return nil
}

// Cloner clones from the world's remotes, as c would from GitHub: the
// recording never runs gh, which would reach GitHub signed in as the user.
type Cloner struct{ w *World }

// Cloner returns the world's cloner.
func (w *World) Cloner() Cloner { return Cloner{w} }

func (c Cloner) Clone(ctx context.Context, _, slug, dir string) error {
	_, name, _ := strings.Cut(slug, "/")
	return c.w.clone(ctx, name, dir)
}

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitx.Run(ctx, dir, args...)
	return err
}

// commit writes file with text in dir and commits it.
func commit(ctx context.Context, dir, file, text, msg string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		return err
	}
	if err := git(ctx, dir, "add", file); err != nil {
		return err
	}
	return git(ctx, dir, "commit", "-q", "-m", msg)
}

// make builds one repository: a bare remote with its history, and for one
// cloned, its clone as it stands before the recording (behind, with
// changes, with worktrees).
func (w *World) make(ctx context.Context, r *repo) error {
	seed := filepath.Join(w.remotes, r.name)
	bare := seed + ".git"
	if err := os.MkdirAll(seed, 0o755); err != nil {
		return err
	}
	w.at(r.pushed + 200*time.Hour)
	if err := git(ctx, seed, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := commit(ctx, seed, "README.md", "# "+r.name+"\n", "Start "+r.name); err != nil {
		return err
	}
	if err := git(ctx, w.remotes, "clone", "-q", "--bare", seed, bare); err != nil {
		return err
	}
	if err := git(ctx, seed, "remote", "add", "origin", bare); err != nil {
		return err
	}
	clone := filepath.Join(w.Root, "github.com", Owner, r.name)
	if r.cloned {
		if err := w.clone(ctx, r.name, clone); err != nil {
			return err
		}
		if err := git(ctx, clone, "fetch", "-q", "origin"); err != nil {
			return err
		}
		// Fetched last a while ago, before GitHub moved on.
		last := w.Built.Add(-(r.pushed + 3*time.Hour))
		if err := os.Chtimes(filepath.Join(clone, ".git", "FETCH_HEAD"), last, last); err != nil {
			return err
		}
	}
	switch r.name {
	case "agent-runner":
		// A branch worked on in a worktree, merged on GitHub since; and
		// one still being worked on, with changes.
		wt := filepath.Join(w.Home, "worktrees", r.name, "retry-backoff")
		w.at(80 * time.Hour)
		if err := git(ctx, clone, "worktree", "add", "-q", "-b", "retry-backoff", wt); err != nil {
			return err
		}
		if err := commit(ctx, wt, "retry.go", "package runner\n", "Retry with backoff"); err != nil {
			return err
		}
		if err := git(ctx, wt, "push", "-q", "origin", "retry-backoff"); err != nil {
			return err
		}
		busy := filepath.Join(w.Home, "worktrees", r.name, "socket-close")
		if err := git(ctx, clone, "worktree", "add", "-q", "-b", "socket-close", busy); err != nil {
			return err
		}
		if err := commit(ctx, busy, "socket.go", "package runner\n", "Notice the socket closing"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(busy, "socket.go"), []byte("package runner\n\n// draft\n"), 0o644); err != nil {
			return err
		}
		w.at(r.pushed)
		if err := git(ctx, seed, "fetch", "-q", "origin", "retry-backoff"); err != nil {
			return err
		}
		if err := git(ctx, seed, "merge", "-q", "--no-ff", "-m", "Merge retry-backoff", "FETCH_HEAD"); err != nil {
			return err
		}
		for i, msg := range []string{"Bump the socket library", "Log the runner's exit", "Read the timeout from the config"} {
			if err := commit(ctx, seed, fmt.Sprintf("change%d.txt", i), msg+"\n", msg); err != nil {
				return err
			}
		}
	}
	// What GitHub has that the clone has not fetched.
	if r.name != "agent-runner" && r.behind > 0 {
		w.at(r.pushed)
		msgs := []string{"Fix the time zone of all-day events", "Keep the sync token between runs", "Read the config once", "Name the flags as the docs do"}
		for i := range r.behind {
			if err := commit(ctx, seed, fmt.Sprintf("change%d.txt", i), msgs[i%len(msgs)]+"\n", msgs[i%len(msgs)]); err != nil {
				return err
			}
		}
	}
	if r.dirty {
		if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# "+r.name+"\n\nwip\n"), 0o644); err != nil {
			return err
		}
	}
	if err := git(ctx, seed, "push", "-q", "origin", "HEAD:main"); err != nil {
		return err
	}
	head, err := gitx.Run(ctx, bare, "rev-parse", "refs/heads/main")
	if err != nil {
		return err
	}
	r.head = strings.TrimSpace(head)
	return nil
}
