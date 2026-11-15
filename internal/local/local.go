// Package local reads the state of a clone from git, collects the facts a
// decision about it is made from, and carries out the changes gh-tidy makes
// to it with git. It never fetches unless asked to, and never decides
// anything: what to do is the policy package's.
package local

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/babarot/gh-tidy/internal/gitx"
	"github.com/babarot/gh-tidy/internal/model"
)

// Find lists the clones directly under dir: directories holding .git, a
// directory or a file (a clone made with --separate-git-dir).
func Find(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Owners lists the owner directories under dir (<root>/<host>), leaving
// out hidden ones.
func Owners(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Scan reads the clone whose main worktree is at path.
func Scan(ctx context.Context, path string) model.Clone {
	c := model.Clone{Key: model.NewCloneKey(path), Path: path, Name: filepath.Base(path)}
	git := func(args ...string) (string, error) { return gitx.Run(ctx, path, args...) }

	out, err := git("status", "--porcelain=v2", "--branch", "--show-stash", "-z")
	if err != nil {
		c.Err = err.Error()
		return c
	}
	c.Status = status(gitx.ParseStatus(out))

	if u, err := git("config", "--get", "remote.origin.url"); err == nil {
		c.OriginURL = u
		if h, s, ok := gitx.Slug(u); ok {
			c.Host, c.Slug, c.Remote = h, s, model.NewRemoteKey(h, s)
		}
	}

	var common string
	var wg sync.WaitGroup
	wg.Go(func() {
		if out, err := git("symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
			c.OriginHEAD = strings.TrimPrefix(out, "origin/")
		}
	})
	wg.Go(func() {
		if out, err := git("for-each-ref", "--format="+gitx.BranchFormat, "refs/heads"); err == nil {
			for _, b := range gitx.ParseBranches(out) {
				c.Branches = append(c.Branches, model.Branch{Name: b.Name, Upstream: b.Upstream, Ahead: b.Ahead, Behind: b.Behind, Gone: b.Gone, Date: unix(b.Date)})
			}
		}
	})
	wg.Go(func() {
		if out, err := git("log", "-1", "--format=%ct%x00%s"); err == nil {
			if ts, subj, ok := strings.Cut(out, "\x00"); ok {
				if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
					c.LastCommit = unix(n)
				}
				c.LastSubject = subj
			}
		}
	})
	wg.Go(func() {
		if out, err := git("rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			common = out
			if fi, err := os.Stat(filepath.Join(out, "FETCH_HEAD")); err == nil {
				c.FetchedAt = fi.ModTime()
			}
		}
	})
	wg.Go(func() {
		out, err := git("worktree", "list", "--porcelain", "-z")
		if err != nil {
			return
		}
		for i, w := range gitx.ParseWorktrees(out) {
			wt := model.Worktree{Path: w.Path, Head: w.Head, Branch: w.Branch, Main: i == 0, Prunable: w.Prunable, Locked: w.Locked, Dirty: -1}
			if !w.Prunable {
				if i == 0 {
					wt.Dirty = c.Dirty() + c.Untracked
				} else if s, err := gitx.Run(ctx, w.Path, "status", "--porcelain", "-z"); err == nil {
					wt.Dirty = strings.Count(s, "\x00")
				}
			}
			c.Worktrees = append(c.Worktrees, wt)
		}
	})
	wg.Wait()

	for i := range c.Worktrees {
		w := &c.Worktrees[i]
		if w.Prunable {
			continue
		}
		dir := gitDir(*w, common)
		w.InProgress = inProgress(dir)
		if w.Branch == "" {
			w.Occupies = occupied(dir)
		}
		if !w.Main {
			w.Created, w.Active = headLogTimes(dir)
		}
	}

	if c.OriginHEAD != "" {
		c.Default, c.DefaultSource = c.OriginHEAD, model.DefaultFromOriginHEAD
	} else {
		var found []string
		for _, b := range []string{"main", "master"} {
			if _, err := git("rev-parse", "-q", "--verify", "refs/remotes/origin/"+b); err == nil {
				found = append(found, b)
			}
		}
		if len(found) == 1 {
			c.Default, c.DefaultSource = found[0], model.DefaultFromGuess
		}
	}
	measureBase(ctx, &c)
	return c
}

// SetDefault makes branch, GitHub's default, the one BASE measures
// against, and measures again when that changes it.
func SetDefault(ctx context.Context, c *model.Clone, branch string) {
	if branch == "" || (branch == c.Default && c.DefaultSource == model.DefaultFromGitHub) {
		return
	}
	changed := branch != c.Default
	c.Default, c.DefaultSource = branch, model.DefaultFromGitHub
	if changed {
		measureBase(ctx, c)
	}
}

func measureBase(ctx context.Context, c *model.Clone) {
	c.BaseBehind, c.DefaultOid = 0, ""
	if c.Default == "" {
		return
	}
	if oid, err := gitx.Run(ctx, c.Path, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+c.Default); err == nil {
		c.DefaultOid = oid
	}
	measureWorktrees(ctx, c)
	if c.Head == "" || c.Head == c.Default || c.Oid == "" {
		return
	}
	for _, ref := range []string{"refs/remotes/origin/" + c.Default, "refs/heads/" + c.Default} {
		out, err := gitx.Run(ctx, c.Path, "rev-list", "--count", "HEAD.."+ref)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(out); err == nil && n > c.BaseBehind {
			c.BaseBehind = n
		}
	}
}

// measureWorktrees counts each linked worktree's branch against the
// default branch: origin/<Default>, or the local one without it. It writes
// a new slice, so that a copy of the clone measured in the background does
// not change the one being drawn.
func measureWorktrees(ctx context.Context, c *model.Clone) {
	base := "refs/remotes/origin/" + c.Default
	if c.DefaultOid == "" {
		base = "refs/heads/" + c.Default
	}
	wts := make([]model.Worktree, len(c.Worktrees))
	copy(wts, c.Worktrees)
	for i, w := range wts {
		wts[i].AheadMain, wts[i].BehindMain, wts[i].Measured = 0, 0, false
		if w.Main || w.Branch == "" || w.Prunable {
			continue
		}
		out, err := gitx.Run(ctx, c.Path, "rev-list", "--left-right", "--count", base+"...refs/heads/"+w.Branch)
		if err != nil {
			continue
		}
		var behind, ahead int
		if _, err := fmt.Sscanf(out, "%d\t%d", &behind, &ahead); err == nil {
			wts[i].AheadMain, wts[i].BehindMain, wts[i].Measured = ahead, behind, true
		}
	}
	c.Worktrees = wts
}

// gitDir is a worktree's own git directory: the common one for the main
// worktree, and for a linked one what its .git file points to. Reading
// the file costs no git process. A linked worktree whose directory is gone
// (moved, or on a disk not mounted) still has its directory under
// <common>/worktrees, found by the gitdir file that points back to it.
func gitDir(w model.Worktree, common string) string {
	if w.Main {
		return common
	}
	data, err := os.ReadFile(filepath.Join(w.Path, ".git"))
	if err != nil {
		return adminDir(common, w.Path)
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(w.Path, dir)
	}
	return dir
}

// adminDir finds the directory under <common>/worktrees whose gitdir file
// points to the worktree at path.
func adminDir(common, path string) string {
	if common == "" {
		return ""
	}
	ents, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return ""
	}
	want := filepath.Join(path, ".git")
	for _, e := range ents {
		dir := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(dir, "gitdir"))
		if err == nil && filepath.Clean(strings.TrimSpace(string(b))) == want {
			return dir
		}
	}
	return ""
}

// inProgress names the operation stopped half way in the worktree whose
// git directory is dir, from the files git leaves while it waits.
func inProgress(dir string) string {
	if dir == "" {
		return ""
	}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return "rebase"
	case exists("MERGE_HEAD"):
		return "merge"
	case exists("CHERRY_PICK_HEAD"):
		return "cherry-pick"
	case exists("REVERT_HEAD"):
		return "revert"
	case exists("BISECT_LOG"):
		return "bisect"
	}
	return ""
}

// occupied is the branch a rebase or a bisect stopped in the worktree
// whose git directory is dir will come back to; empty when none.
func occupied(dir string) string {
	if dir == "" {
		return ""
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	for _, f := range []string{"rebase-merge/head-name", "rebase-apply/head-name"} {
		if b, ok := strings.CutPrefix(read(f), "refs/heads/"); ok {
			return b
		}
	}
	// BISECT_START holds the branch bisect started from, or a commit when
	// it started detached; a commit matches no branch name.
	return strings.TrimPrefix(read("BISECT_START"), "refs/heads/")
}

// headLogTimes reads when a linked worktree was made and when a commit or
// a checkout last happened in it: the first and last entries of its own
// HEAD log. Without the log (expired, or never written), the made time is
// when its gitdir file was written, which git does once, on creation.
func headLogTimes(dir string) (created, active time.Time) {
	if dir == "" {
		return
	}
	if f, err := os.Open(filepath.Join(dir, "logs", "HEAD")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			if t, ok := reflogTime(sc.Text()); ok {
				if created.IsZero() {
					created = t
				}
				active = t
			}
		}
		// A log cut short keeps the entries read before the break.
		_ = sc.Err()
		_ = f.Close()
	}
	if created.IsZero() {
		if fi, err := os.Stat(filepath.Join(dir, "gitdir")); err == nil {
			created = fi.ModTime()
		}
	}
	return created, active
}

// reflogTime reads the time of one reflog line: "<old> <new> <who> <unix>
// <tz>\t<message>", where <who> ends with the e-mail in angle brackets.
func reflogTime(line string) (time.Time, bool) {
	head, _, _ := strings.Cut(line, "\t")
	i := strings.LastIndex(head, "> ")
	if i < 0 {
		return time.Time{}, false
	}
	f := strings.Fields(head[i+2:])
	if len(f) == 0 {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

func status(s gitx.Status) model.Status {
	return model.Status{Head: s.Head, Oid: s.Oid, Upstream: s.Upstream, Ahead: s.Ahead, Behind: s.Behind, HasAB: s.HasAB,
		Staged: s.Staged, Unstaged: s.Unstaged, Untracked: s.Untracked, Conflicts: s.Conflicts, Stash: s.Stash}
}

func unix(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}
