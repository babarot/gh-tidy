// Package locate decides whose clones to show from where gh tidy is
// started. Clones are laid out as ghq lays them out, <root>/<host>/<owner>/<repo>;
// ghq itself is not needed, only its root.
package locate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/babarot/gh-tidy/internal/gitx"
)

// DefaultHost is the host clones are looked up under.
const DefaultHost = "github.com"

// Root returns where clones live: configured when not empty, else the first
// ghq.root in git config, else ~/ghq.
func Root(ctx context.Context, configured string) string {
	if configured != "" {
		return ExpandHome(configured)
	}
	if out, err := gitx.Run(ctx, ".", "config", "--path", "--get-all", "ghq.root"); err == nil {
		if first, _, _ := strings.Cut(out, "\n"); first != "" {
			return ExpandHome(first)
		}
	}
	return filepath.Join(home(), "ghq")
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

// Dir is the owner's directory under root.
func (t Target) Dir(root string) string { return filepath.Join(root, t.Host, t.Owner) }

// ErrOutside is returned by Resolve when cwd is outside root and is no
// clone, so only a configured default owner or the picker can be opened.
var ErrOutside = errors.New("outside the root")

// Resolve decides what to open from cwd:
//   - inside a clone or one of its worktrees: the owner of its origin, with
//     that clone selected
//   - <root>/<host>/<owner> or below: that owner
//   - <root> or <root>/<host>: the owner picker
//   - anywhere else: ErrOutside
func Resolve(ctx context.Context, cwd, root string) (Target, error) {
	cwd, root = real(cwd), real(root)
	if t, ok := fromClone(ctx, cwd, root); ok {
		return t, nil
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return Target{}, ErrOutside
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

// fromClone resolves cwd inside a git work tree, a linked worktree included,
// through the main worktree it belongs to.
func fromClone(ctx context.Context, cwd, root string) (Target, bool) {
	common, err := gitx.Run(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Target{}, false
	}
	main := filepath.Dir(real(common))
	if filepath.Base(common) != ".git" {
		main = real(common) // a bare repository or a separate git dir
	}
	if u, err := gitx.Run(ctx, main, "config", "--get", "remote.origin.url"); err == nil {
		if host, slug, ok := gitx.Slug(u); ok {
			owner, _, _ := strings.Cut(slug, "/")
			// The directory name may differ from the origin's (a renamed
			// repository); the clone is selected by its path.
			t := Target{Kind: Owner, Host: host, Owner: owner}
			if filepath.Dir(main) == filepath.Join(root, host, owner) {
				t.Select = main
			}
			return t, true
		}
	}
	// No GitHub origin: the owner is where the clone sits under root.
	rel, err := filepath.Rel(root, main)
	if err != nil {
		return Target{}, false
	}
	if parts := strings.Split(rel, string(filepath.Separator)); len(parts) == 3 && parts[0] != ".." {
		return Target{Kind: Owner, Host: parts[0], Owner: parts[1], Select: main}, true
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

// canon resolves symlinks (macOS's /tmp is /private/tmp), keeping p when it
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
