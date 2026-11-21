// Package recording is the made-up world the demo recording runs gh tidy
// against: real git clones and remotes in a temporary directory, and a
// made-up GitHub that answers from them. Every person, repository and
// package in it is made up. It is never part of gh tidy (ADR-010).
package recording

import (
	"context"
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

// Build makes the world under dir. The caller has set the environment so
// that git reads none of the user's settings (see the demo's main).
func Build(ctx context.Context, dir string) (*World, error) {
	home, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	w := &World{Home: home, Root: filepath.Join(home, "src"), Config: filepath.Join(home, "config.toml"), Built: time.Now(),
		remotes: filepath.Join(home, "remotes")}
	w.repos = []repo{
		{name: "agent-runner", cloned: true, stars: 128, pushed: 2*time.Hour + 30*time.Minute, ci: "SUCCESS",
			issues: []opened{{41, "Retry the runner when the socket closes", "kai", 3*time.Hour + 10*time.Minute}, {37, "Document the cache layout", "ren", 52 * time.Hour}},
			prs:    []opened{{40, "Speed up status parsing", "hana", 75 * time.Hour}}},
		{name: "calendar-sync", cloned: true, stars: 41, pushed: 26 * time.Hour, ci: "FAILURE",
			alerts: []alert{{"critical", "ical-parser", "npm", "< 2.4.1", "2.4.1", "package-lock.json"}, {"moderate", "tmp-dir", "npm", "<= 0.9.0", "", "package-lock.json"}},
			issues: []opened{{12, "Events after midnight land on the wrong day", "ren", 98 * time.Hour}}},
		{name: "dotfiles", cloned: true, stars: 16, pushed: 5*time.Hour + 20*time.Minute, ci: "SUCCESS", private: true},
		{name: "pixel-garden", cloned: true, stars: 9, pushed: 20*time.Hour + 10*time.Minute, ci: "SUCCESS"},
		{name: "trail-notes", cloned: true, stars: 37, pushed: 30 * time.Hour, ci: "PENDING",
			prs: []opened{{8, "Add a map of the trail", "hana", 54 * time.Hour}}},
		{name: "tool-shed", stars: 23, pushed: 100 * time.Hour, ci: "SUCCESS",
			alerts: []alert{{"high", "yaml-lite", "go", "< 1.3.0", "1.3.0", "go.mod"}},
			issues: []opened{{3, "Explain the shelf layout", "kai", 120 * time.Hour}}},
		{name: "weather-cli", cloned: true, stars: 4, pushed: 230 * time.Hour, ci: "SUCCESS"},
	}
	for i := range w.repos {
		if err := w.make(ctx, &w.repos[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", w.repos[i].name, err)
		}
	}
	cfg := fmt.Sprintf("[core]\nroots = [%q]\n\n[tui]\ntheme = \"catppuccin-mocha\"\n", w.Root)
	if err := os.WriteFile(w.Config, []byte(cfg), 0o644); err != nil {
		return nil, err
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
		if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
			return err
		}
		if err := git(ctx, filepath.Dir(clone), "clone", "-q", bare, clone); err != nil {
			return err
		}
		// The origin is GitHub's name, sent to the bare remote here.
		url := "git@github.com:" + Owner + "/" + r.name + ".git"
		for _, args := range [][]string{{"remote", "set-url", "origin", url}, {"config", "url." + bare + ".insteadOf", url}, {"fetch", "-q", "origin"}} {
			if err := git(ctx, clone, args...); err != nil {
				return err
			}
		}
		// Fetched last a while ago, before GitHub moved on.
		last := w.Built.Add(-(r.pushed/2 + 3*time.Hour))
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
	case "calendar-sync", "trail-notes":
		w.at(r.pushed)
		for i, msg := range []string{"Fix the time zone of all-day events", "Keep the sync token between runs"} {
			if err := commit(ctx, seed, fmt.Sprintf("change%d.txt", i), msg+"\n", msg); err != nil {
				return err
			}
		}
	case "dotfiles":
		if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# dotfiles\n\nwip\n"), 0o644); err != nil {
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
