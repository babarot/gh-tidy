// Package locate decides whose clones to show from where gh tidy is
// started. Clones are laid out as <root>/<host>/<owner>/<repo>, under the
// roots the settings name; no tool that manages clones is read for them
// (ADR-008).
package locate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/babarot/gh-tidy/internal/gitx"
)

// DefaultHost is the host clones are looked up under.
const DefaultHost = "github.com"

// Roots returns the roots the settings name, with ~ expanded and symlinks
// resolved, each once, in their order. A root inside another is a
// mistake: which of the two is meant is not known.
func Roots(configured []string) ([]string, error) {
	var out []string
	for _, c := range configured {
		r := real(ExpandHome(c))
		dup := false
		for _, o := range out {
			switch {
			case o == r:
				dup = true
			case within(o, r) || within(r, o):
				return nil, fmt.Errorf("[core] roots: %s is inside %s; name one of them", Abbrev(longer(o, r)), Abbrev(shorter(o, r)))
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out, nil
}

// within reports whether p is below dir.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func longer(a, b string) string {
	if len(a) > len(b) {
		return a
	}
	return b
}

func shorter(a, b string) string {
	if len(a) > len(b) {
		return b
	}
	return a
}

// Candidate is the root the clone cwd is in would have: X when its main
// worktree is X/<host>/<owner>/<dir> for its origin's host and owner,
// in any case; the directory's own name is not compared, for a renamed
// repository. It is only offered to the user, never used unasked.
func Candidate(ctx context.Context, cwd string) (string, bool) {
	main, ok := mainWorktree(ctx, cwd)
	if !ok {
		return "", false
	}
	u, err := gitx.Run(ctx, main, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", false
	}
	host, slug, ok := gitx.Slug(u)
	if !ok {
		return "", false
	}
	owner, _, _ := strings.Cut(slug, "/")
	ownerDir := filepath.Dir(main)
	hostDir := filepath.Dir(ownerDir)
	if !strings.EqualFold(filepath.Base(ownerDir), owner) || !strings.EqualFold(filepath.Base(hostDir), host) {
		return "", false
	}
	return filepath.Dir(hostDir), true
}

// Kind is what to open.
type Kind int

const (
	// Owner opens one owner's clones.
	Owner Kind = iota
	// Picker is the root or a host directory under it, which name no
	// owner of their own.
	Picker
)

// Target is what to open.
type Target struct {
	Kind  Kind
	Host  string
	Owner string
	// Select is the clone to start on, the one started in; empty for none.
	Select string
}

// ErrOutside is returned by Resolve when cwd is under no root and is no
// clone.
var ErrOutside = errors.New("outside the root")

// Resolve decides what to open from cwd, with roots resolved as Roots
// returns them:
//   - inside a clone or one of its worktrees: the owner of its origin, with
//     that clone selected when it sits under a root
//   - <root>/<host>/<owner> or below: that owner
//   - <root> or <root>/<host>: the owner picker
//   - anywhere else: ErrOutside
func Resolve(ctx context.Context, cwd string, roots []string) (Target, error) {
	cwd = real(cwd)
	if t, ok := fromClone(ctx, cwd, roots); ok {
		return t, nil
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root, cwd)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		switch {
		case rel == ".":
			return Target{Kind: Picker, Host: DefaultHost}, nil
		case len(parts) == 1:
			return Target{Kind: Picker, Host: parts[0]}, nil
		}
		return Target{Kind: Owner, Host: parts[0], Owner: parts[1]}, nil
	}
	return Target{}, ErrOutside
}

// mainWorktree is the main worktree of the git work tree cwd is in, a
// linked worktree included.
func mainWorktree(ctx context.Context, cwd string) (string, bool) {
	common, err := gitx.Run(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", false
	}
	if filepath.Base(common) != ".git" {
		return real(common), true // a bare repository or a separate git dir
	}
	return filepath.Dir(real(common)), true
}

// fromClone resolves cwd inside a git work tree, a linked worktree included,
// through the main worktree it belongs to.
func fromClone(ctx context.Context, cwd string, roots []string) (Target, bool) {
	main, ok := mainWorktree(ctx, cwd)
	if !ok {
		return Target{}, false
	}
	if u, err := gitx.Run(ctx, main, "config", "--get", "remote.origin.url"); err == nil {
		if host, slug, ok := gitx.Slug(u); ok {
			owner, _, _ := strings.Cut(slug, "/")
			// The directory name may differ from the origin's (a renamed
			// repository); the clone is selected by its path.
			t := Target{Kind: Owner, Host: host, Owner: owner}
			for _, root := range roots {
				if filepath.Dir(main) == filepath.Join(root, host, owner) {
					t.Select = main
				}
			}
			return t, true
		}
	}
	// No GitHub origin: the owner is where the clone sits under a root.
	for _, root := range roots {
		rel, err := filepath.Rel(root, main)
		if err != nil {
			continue
		}
		if parts := strings.Split(rel, string(filepath.Separator)); len(parts) == 3 && parts[0] != ".." {
			return Target{Kind: Owner, Host: parts[0], Owner: parts[1], Select: main}, true
		}
	}
	return Target{}, false
}

// ExpandHome expands a leading ~/.
func ExpandHome(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}

// Abbrev writes p with ~ for the home directory.
func Abbrev(p string) string {
	h := home()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

// real resolves symlinks (macOS's /tmp is /private/tmp), keeping p when it
// cannot be resolved.
func real(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
