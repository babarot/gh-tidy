// Package gittest builds real git repositories in a test's temporary
// directory: a bare "GitHub" remote and clones of it.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Git runs git in dir, failing the test on error, and returns its output.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Isolate keeps git run by the code under test away from the user's
// config, for the rest of the test.
func Isolate(t testing.TB) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.com")
}

// Commit writes file in dir and commits it.
func Commit(t testing.TB, dir, file, msg string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "add", file)
	Git(t, dir, "commit", "-q", "-m", msg)
}

// Remote makes a bare repository with one commit on main, and returns its path.
func Remote(t testing.TB, base, name string) string {
	t.Helper()
	bare := filepath.Join(base, "remotes", name+".git")
	seed := filepath.Join(base, "seed-"+name)
	Git(t, base, "init", "-q", "--bare", "-b", "main", bare)
	Git(t, base, "init", "-q", "-b", "main", seed)
	Commit(t, seed, "README", "init")
	Git(t, seed, "remote", "add", "origin", bare)
	Git(t, seed, "push", "-q", "origin", "main")
	return bare
}

// Clone clones remote into dir and points origin's URL at a GitHub-style
// URL for slug, keeping the real one for fetches via pushurl/insteadOf.
func Clone(t testing.TB, remote, dir, slug string) {
	t.Helper()
	Git(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	if slug != "" {
		url := "git@github.com:" + slug + ".git"
		Git(t, dir, "remote", "set-url", "origin", url)
		Git(t, dir, "config", "url."+remote+".insteadOf", url)
	}
}
