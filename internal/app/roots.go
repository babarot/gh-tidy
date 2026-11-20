package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/babarot/gh-tidy/internal/locate"
)

// ask is where a question about the settings can be asked: in on a
// terminal, out to the terminal, never to stdout, which --json writes to.
type ask struct {
	in       io.Reader
	out      io.Writer
	terminal bool
}

// roots are the roots to read, from the settings. With none set, gh tidy
// does not guess: it offers the root of the clone it starts in, and on a
// yes writes it to a config file that does not exist yet. Otherwise it
// stops with the line to write (ADR-008).
func roots(ctx context.Context, configured []string, path, cwd string, a ask) ([]string, error) {
	if len(configured) > 0 {
		return locate.Roots(configured)
	}
	cand, ok := locate.Candidate(ctx, cwd)
	_, statErr := os.Stat(path)
	exists := !errors.Is(statErr, fs.ErrNotExist)
	if ok && a.terminal && !exists {
		line := rootsLine(cand)
		fmt.Fprintf(a.out, "gh tidy: no roots set. Your clones seem to live in %s.\nWrite %s to %s? [y/N] ", locate.Abbrev(cand), line, locate.Abbrev(path))
		answer, _ := bufio.NewReader(a.in).ReadString('\n')
		if s := strings.ToLower(strings.TrimSpace(answer)); s == "y" || s == "yes" {
			if err := writeRoots(path, line); err != nil {
				return nil, fmt.Errorf("could not write %s (%w); add to it:\n\n  [core]\n  %s", locate.Abbrev(path), err, line)
			}
			fmt.Fprintf(a.out, "wrote %s\n", locate.Abbrev(path))
			return locate.Roots([]string{cand})
		}
	}
	if ok {
		return nil, fmt.Errorf("no roots set. Your clones seem to live in %s; add to %s:\n\n  [core]\n  %s", locate.Abbrev(cand), locate.Abbrev(path), rootsLine(cand))
	}
	return nil, fmt.Errorf("no roots set: write where your clones live, each laid out as <host>/<owner>/<repo>, in %s:\n\n  [core]\n  %s", locate.Abbrev(path), rootsLine(filepath.Join(home(), "src")))
}

// rootsLine is the setting naming root, as the file would hold it.
func rootsLine(root string) string { return fmt.Sprintf("roots = [%q]", locate.Abbrev(root)) }

// writeRoots makes the config file holding line alone; a file that came
// meanwhile is left as it is.
func writeRoots(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "[core]\n%s\n", line); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
