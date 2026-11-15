// Package command runs the programs other than git: gh to clone, the
// user's [[actions]] and worktree_remove, and the system's ways to open a
// URL, show a file and copy text. gh tidy names no other tool in its code.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Run runs argv in dir and returns what it wrote, trimmed. A failure
// says the last line it wrote, where programs put what went wrong.
func Run(ctx context.Context, dir string, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("no command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s: %s", argv[0], lastLine(s))
		}
		return s, fmt.Errorf("%s: %w", argv[0], err)
	}
	return s, nil
}

func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// Clone clones host/slug into dir with gh, which brings the user's
// authentication and protocol.
func Clone(ctx context.Context, host, slug, dir string) error {
	repo := slug
	if host != "" && host != "github.com" {
		repo = host + "/" + slug
	}
	_, err := Run(ctx, "", []string{"gh", "repo", "clone", repo, dir, "--", "--quiet"})
	return err
}

// Interactive is a program given the terminal: one that asks or draws.
// It has the methods Bubble Tea's Exec takes, so that the screen can hand
// it the terminal without running programs itself.
type Interactive struct{ cmd *exec.Cmd }

// NewInteractive makes argv, to run in dir.
func NewInteractive(dir string, argv []string) *Interactive {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	return &Interactive{cmd: cmd}
}

func (c *Interactive) Run() error            { return c.cmd.Run() }
func (c *Interactive) SetStdin(r io.Reader)  { c.cmd.Stdin = r }
func (c *Interactive) SetStdout(w io.Writer) { c.cmd.Stdout = w }
func (c *Interactive) SetStderr(w io.Writer) { c.cmd.Stderr = w }

// OpenURL opens url in the browser.
func OpenURL(url string) error {
	argv := []string{"xdg-open", url}
	if runtime.GOOS == "darwin" {
		argv = []string{"open", url}
	}
	return exec.Command(argv[0], argv[1:]...).Run()
}

// Reveal shows path in the file manager; only on macOS, where Finder can
// select it.
func Reveal(path string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("revealing a file is for macOS")
	}
	return exec.Command("open", "-R", path).Run()
}

// Copy puts text on the clipboard with pbcopy on macOS, for terminals that
// ignore the escape sequence the screen also sends; elsewhere it does
// nothing.
func Copy(text string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	path, err := exec.LookPath("pbcopy")
	if err != nil {
		return nil
	}
	c := exec.Command(path)
	c.Stdin = strings.NewReader(text)
	return c.Run()
}
