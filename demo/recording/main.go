// Command recording runs gh tidy against a made-up world, for the
// recording in the README: real git clones in a temporary directory and a
// made-up GitHub (ADR-010). It is never built into gh tidy.
//
//	go run ./demo/recording
//
// demo/recording/tapes/demo.tape records it with VHS (make demo).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/babarot/gh-tidy/demo/internal/recording"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "recording:", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := os.MkdirTemp("", "gh-tidy-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// git and gh read none of the user's settings and sign-in, git makes
	// commits as a made-up person, and nothing reaches beyond the disk: the
	// origins name GitHub, and only a per-clone insteadOf sends them to
	// the remotes here.
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "Mika", "GIT_AUTHOR_EMAIL": "mika@example.com", "GIT_COMMITTER_NAME": "Mika", "GIT_COMMITTER_EMAIL": "mika@example.com",
		"GIT_ALLOW_PROTOCOL": "file", "HOME": dir,
		"XDG_CONFIG_HOME": filepath.Join(dir, ".config"), "XDG_CACHE_HOME": filepath.Join(dir, ".cache"),
		"XDG_STATE_HOME": filepath.Join(dir, ".local", "state"), "GH_CONFIG_DIR": filepath.Join(dir, ".config", "gh"),
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	w, err := recording.Build(ctx, dir)
	if err != nil {
		return err
	}
	if err := os.Setenv("HOME", w.Home); err != nil {
		return err
	}
	return recording.Run(ctx, w)
}
