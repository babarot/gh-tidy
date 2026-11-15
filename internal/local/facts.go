package local

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/babarot/gh-tidy/internal/gitx"
	"github.com/babarot/gh-tidy/internal/model"
)

// AdvanceFacts reads what deciding whether to move the clone at path's
// default branch (name, from source) forward needs. Read it right before
// the decision, in the clone's lane: which worktree holds the branch, and
// whether that worktree has changes, can change at any time.
func AdvanceFacts(ctx context.Context, path, name string, source model.DefaultSource) model.AdvanceFacts {
	f := model.AdvanceFacts{Default: name, DefaultSource: source}
	if name == "" {
		return f
	}
	git := func(args ...string) (string, error) { return gitx.Run(ctx, path, args...) }
	local, remote := "refs/heads/"+name, "refs/remotes/origin/"+name
	_, err := git("rev-parse", "-q", "--verify", local)
	f.LocalExists = err == nil
	_, err = git("rev-parse", "-q", "--verify", remote)
	f.RemoteExists = err == nil
	if f.LocalExists {
		if up, err := git("for-each-ref", "--format=%(upstream:short)", local); err == nil {
			f.Upstream = up
		}
	}
	if f.LocalExists && f.RemoteExists {
		if out, err := git("rev-list", "--count", local+".."+remote); err == nil {
			f.Behind, _ = strconv.Atoi(out)
		}
		_, err := git("merge-base", "--is-ancestor", local, remote)
		f.FastForward = err == nil
	}
	common, _ := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if out, err := git("worktree", "list", "--porcelain", "-z"); err == nil {
		for i, gw := range gitx.ParseWorktrees(out) {
			w := model.Worktree{Path: gw.Path, Branch: gw.Branch, Main: i == 0, Prunable: gw.Prunable}
			if w.Branch == "" {
				w.Occupies = occupied(gitDir(w, common))
			}
			if w.Holds() != name {
				continue
			}
			f.Holders++
			if w.Prunable {
				f.HeldByMissing = true
				continue
			}
			if f.CheckedOut != "" {
				continue
			}
			f.CheckedOut = w.Path
			// Only changes to tracked files are in the way: a fast-forward
			// merge refuses by itself to overwrite an untracked file.
			if s, err := gitx.Run(ctx, w.Path, "status", "--porcelain", "-z", "--untracked-files=no"); err == nil {
				f.Dirty = strings.Count(s, "\x00")
			} else {
				f.Dirty = -1 // unreadable counts as in the way
			}
			f.InProgress = inProgress(gitDir(w, common))
		}
	}
	return f
}

// CleanupFacts reads what deciding what d removes from c needs. Read it
// right before the decision, in the clone's lane.
func CleanupFacts(ctx context.Context, c model.Clone) (model.CleanupFacts, error) {
	f := model.CleanupFacts{Merged: map[string]bool{}, WorkedOn: map[string]bool{}}
	if c.Default == "" {
		return f, ErrNoDefault
	}
	git := func(args ...string) (string, error) { return gitx.Run(ctx, c.Path, args...) }
	f.Base = "origin/" + c.Default
	if _, err := git("rev-parse", "-q", "--verify", "refs/remotes/"+f.Base); err != nil {
		f.Base = c.Default
		if _, err := git("rev-parse", "-q", "--verify", "refs/heads/"+f.Base); err != nil {
			return f, fmt.Errorf("neither origin/%s nor %s exists", c.Default, c.Default)
		}
	}
	out, err := git("for-each-ref", "--merged", f.Base, "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return f, err
	}
	for b := range strings.SplitSeq(out, "\n") {
		if b != "" {
			f.Merged[b] = true
		}
	}
	for _, b := range c.Branches {
		f.WorkedOn[b.Name] = workedOn(ctx, c.Path, b.Name)
	}
	return f, nil
}

// ErrNoDefault is returned when the clone's default branch is unknown.
var ErrNoDefault = errors.New("default branch unknown (run git remote set-head origin -a)")

// workedOn reports whether branch got a commit after it was created: its
// reflog has an entry other than its creation. Without a reflog (expired)
// the branch is old enough to count as worked on.
func workedOn(ctx context.Context, dir, branch string) bool {
	out, err := gitx.Run(ctx, dir, "reflog", "show", "--format=%gs", "refs/heads/"+branch, "--")
	if err != nil || strings.TrimSpace(out) == "" {
		return true
	}
	for l := range strings.SplitSeq(out, "\n") {
		if l != "" && !strings.HasPrefix(l, "branch: Created from") {
			return true
		}
	}
	return false
}

// Fetch fetches origin into the clone at path, pruning deleted branches.
func Fetch(ctx context.Context, path string) error {
	_, err := gitx.Run(ctx, path, "fetch", "--prune", "--quiet", "origin")
	return err
}

// FastForwardRef moves the local branch name to origin/name in the clone at
// path, without checking it out. The refspec has no +, so git refuses
// anything but a fast-forward. The caller has checked that no worktree
// holds the branch: git versions differ in whether they refuse that
// themselves.
func FastForwardRef(ctx context.Context, path, name string) error {
	_, err := gitx.Run(ctx, path, "fetch", "--quiet", ".", "refs/remotes/origin/"+name+":refs/heads/"+name)
	return err
}

// MergeFastForward moves the branch checked out in the worktree at wt to
// origin/name, refusing anything but a fast-forward. Unlike a bare pull it
// does not follow the branch's own upstream, which may be another remote.
func MergeFastForward(ctx context.Context, wt, name string) error {
	_, err := gitx.Run(ctx, wt, "merge", "--ff-only", "--quiet", "refs/remotes/origin/"+name)
	return err
}

// WorktreeDirty counts the changed and untracked files of the worktree at
// wt, read now: the check right before a worktree is removed.
func WorktreeDirty(ctx context.Context, wt string) (int, error) {
	s, err := gitx.Run(ctx, wt, "status", "--porcelain", "-z")
	if err != nil {
		return 0, err
	}
	return strings.Count(s, "\x00"), nil
}

// DeleteMergedBranch deletes branch in the clone at path after checking
// again that base has it. git branch -d would measure against HEAD or the
// branch's upstream instead, and refuse a branch merged on GitHub while the
// local default branch is behind.
func DeleteMergedBranch(ctx context.Context, path, branch, base string) error {
	if _, err := gitx.Run(ctx, path, "merge-base", "--is-ancestor", "refs/heads/"+branch, base); err != nil {
		return fmt.Errorf("%s is no longer merged into %s", branch, base)
	}
	_, err := gitx.Run(ctx, path, "branch", "-D", branch)
	return err
}

// BranchExists reports whether the local branch exists in the clone at
// path: a worktree's own removal command may have deleted it already.
func BranchExists(ctx context.Context, path, branch string) bool {
	_, err := gitx.Run(ctx, path, "rev-parse", "-q", "--verify", "refs/heads/"+branch)
	return err == nil
}

// DeleteBranch deletes branch whether or not it is merged: only for what a
// setting asks for, such as a branch whose upstream is gone.
func DeleteBranch(ctx context.Context, path, branch string) error {
	_, err := gitx.Run(ctx, path, "branch", "-D", branch)
	return err
}

// PruneWorktrees forgets the worktrees whose directories are gone.
func PruneWorktrees(ctx context.Context, path string) error {
	_, err := gitx.Run(ctx, path, "worktree", "prune")
	return err
}

// RemoteAhead decides whether GitHub's default branch, at remoteOid, has
// commits the clone at path has not fetched: remoteOid is not in the clone,
// or is not an ancestor of its origin/<name>. Equal commits cost no git
// process. A commit GitHub has that the clone lacks makes git's ancestry
// check fail with 128 rather than 1; either way it is ahead.
func RemoteAhead(ctx context.Context, path, name, remoteOid string) model.Ahead {
	local, _ := gitx.Run(ctx, path, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+name)
	a := model.Ahead{Remote: remoteOid, Local: local}
	switch {
	case remoteOid == "":
	case remoteOid == local:
	case local == "":
		a.Ahead = true
	default:
		_, err := gitx.Run(ctx, path, "merge-base", "--is-ancestor", remoteOid, local)
		a.Ahead = err != nil
	}
	return a
}
