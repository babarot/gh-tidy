// Package cleanup decides which worktrees and branches of a clone are done
// with, says why it keeps the others, and removes the ones it decided on.
// Nothing is removed by force: a worktree with changes, one never committed
// to, or the one gh fleet runs in stays.
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/gitx"
	"github.com/babarot/gh-fleet/internal/repo"
)

// Kind is what an item is.
type Kind int

const (
	Worktree Kind = iota // a linked worktree and its branch
	Prunable             // a worktree whose directory is gone
	Branch               // a branch checked out nowhere
)

// Item is one thing to remove or keep.
type Item struct {
	Kind   Kind
	Path   string // worktrees
	Branch string
	// Why is how it is removed, or why it is kept.
	Why string
	// Force deletes the branch with -D: its upstream is gone but it is not
	// merged, and [cleanup] delete_gone asks for it.
	Force bool
}

// Plan is what d would do to one clone.
type Plan struct {
	Base   string // the ref merged is measured against, such as origin/main
	Remove []Item
	Keep   []Item
}

// ErrNoDefault is returned when the clone's default branch is unknown.
var ErrNoDefault = errors.New("default branch unknown (run git remote set-head origin -a)")

// Decide plans the cleanup of r. cwd is where gh fleet runs: the worktree
// holding it is kept.
func Decide(ctx context.Context, r *repo.Repo, cfg config.Cleanup, cwd string) (*Plan, error) {
	if r.Default == "" {
		return nil, ErrNoDefault
	}
	git := func(args ...string) (string, error) { return gitx.Run(ctx, r.Path, args...) }
	base := "origin/" + r.Default
	if _, err := git("rev-parse", "-q", "--verify", "refs/remotes/"+base); err != nil {
		base = r.Default
		if _, err := git("rev-parse", "-q", "--verify", "refs/heads/"+base); err != nil {
			return nil, fmt.Errorf("neither origin/%s nor %s exists", r.Default, r.Default)
		}
	}
	out, err := git("for-each-ref", "--merged", base, "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	merged := map[string]bool{}
	for _, b := range strings.Split(out, "\n") {
		if b != "" {
			merged[b] = true
		}
	}
	protected := func(b string) bool {
		return b == r.Default || b == r.Head || slices.Contains(cfg.Protect, b)
	}
	p := &Plan{Base: base}

	checkedOut := map[string]bool{}
	for _, w := range r.Worktrees {
		if w.Branch != "" {
			checkedOut[w.Branch] = true
		}
	}
	for _, w := range r.Worktrees {
		if w.Main {
			continue
		}
		item := Item{Kind: Worktree, Path: w.Path, Branch: w.Branch}
		if w.Prunable {
			item.Kind, item.Why = Prunable, "directory gone · worktree prune"
			p.Remove = append(p.Remove, item)
			continue
		}
		if w.Branch == "" || !merged[w.Branch] {
			continue // still has work of its own, or detached: not d's to judge
		}
		keep := func(why string) { item.Why = why; p.Keep = append(p.Keep, item) }
		switch {
		case within(cwd, w.Path):
			keep("you are in it")
		case w.Locked:
			keep("locked")
		case protected(w.Branch):
			keep("protected branch")
		default:
			dirty, err := gitx.Run(ctx, w.Path, "status", "--porcelain", "-z")
			switch {
			case err != nil:
				keep("unreadable: " + err.Error())
			case dirty != "":
				n := strings.Count(dirty, "\x00")
				keep(fmt.Sprintf("%d uncommitted change%s", n, plural(n)))
			case !workedOn(ctx, r.Path, w.Branch):
				keep("no commits since it was created; may be in use")
			default:
				item.Why = "merged · clean"
				p.Remove = append(p.Remove, item)
			}
		}
	}

	for _, b := range r.Branches {
		if checkedOut[b.Name] || protected(b.Name) {
			continue
		}
		switch {
		case merged[b.Name]:
			p.Remove = append(p.Remove, Item{Kind: Branch, Branch: b.Name, Why: "merged"})
		case b.Gone && cfg.DeleteGone:
			p.Remove = append(p.Remove, Item{Kind: Branch, Branch: b.Name, Why: "upstream gone · branch -D", Force: true})
		case b.Gone:
			p.Keep = append(p.Keep, Item{Kind: Branch, Branch: b.Name, Why: "upstream gone but not merged (squash merge?); delete_gone = true removes it"})
		}
	}
	return p, nil
}

// workedOn reports whether branch got a commit after it was created: its
// reflog has an entry other than its creation. A branch made for a worktree
// and never committed to points at a commit of the default branch, so it
// looks merged, though it may be in use. Without a reflog (expired) the
// branch is old enough to count as worked on.
func workedOn(ctx context.Context, dir, branch string) bool {
	out, err := gitx.Run(ctx, dir, "reflog", "show", "--format=%gs", "refs/heads/"+branch, "--")
	if err != nil || strings.TrimSpace(out) == "" {
		return true
	}
	for _, l := range strings.Split(out, "\n") {
		if l != "" && !strings.HasPrefix(l, "branch: Created from") {
			return true
		}
	}
	return false
}

// Run carries out p, in order: prune, worktrees, branches. It stops at the
// first failure and returns how many items it removed.
func Run(ctx context.Context, r *repo.Repo, p *Plan, cfg config.Cleanup, vars map[string]string) (int, error) {
	git := func(args ...string) error { _, err := gitx.Run(ctx, r.Path, args...); return err }
	done := 0
	pruned := false
	for _, it := range p.Remove {
		switch it.Kind {
		case Prunable:
			if !pruned {
				if err := git("worktree", "prune"); err != nil {
					return done, err
				}
				pruned = true
			}
		case Worktree:
			v := map[string]string{"path": it.Path, "branch": it.Branch}
			for k, x := range vars {
				if _, set := v[k]; !set {
					v[k] = x
				}
			}
			argv := config.Expand(cfg.WorktreeRemove, v)
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = r.Path
			if out, err := cmd.CombinedOutput(); err != nil {
				return done, fmt.Errorf("%s: %s", strings.Join(argv, " "), lastLine(out, err))
			}
			// The command may have deleted the branch with the worktree.
			if _, err := gitx.Run(ctx, r.Path, "rev-parse", "-q", "--verify", "refs/heads/"+it.Branch); err == nil {
				if err := deleteMerged(ctx, r.Path, it.Branch, p.Base); err != nil {
					return done, err
				}
			}
		case Branch:
			var err error
			if it.Force {
				err = git("branch", "-D", it.Branch)
			} else {
				err = deleteMerged(ctx, r.Path, it.Branch, p.Base)
			}
			if err != nil {
				return done, err
			}
		}
		done++
	}
	return done, nil
}

// deleteMerged deletes branch after checking again that base has it. git
// branch -d would measure against HEAD or the branch's upstream instead, and
// refuse a branch merged on GitHub while the local default branch is behind.
func deleteMerged(ctx context.Context, dir, branch, base string) error {
	if _, err := gitx.Run(ctx, dir, "merge-base", "--is-ancestor", "refs/heads/"+branch, base); err != nil {
		return fmt.Errorf("%s is no longer merged into %s", branch, base)
	}
	_, err := gitx.Run(ctx, dir, "branch", "-D", branch)
	return err
}

func within(p, dir string) bool {
	if p == "" {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func lastLine(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return err.Error()
	}
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}
