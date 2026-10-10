// Package repo reads the state of one local clone from git: where it is
// against its upstream and its default branch, what is not committed or not
// pushed, and its worktrees. It reads only what is on the machine; it never
// fetches.
package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/babarot/gh-tidy/internal/gitx"
)

// Where the default branch came from, most trusted first.
const (
	DefaultFromGitHub     = "github"      // the repository's default branch on GitHub
	DefaultFromOriginHEAD = "origin/HEAD" // refs/remotes/origin/HEAD
	DefaultFromGuess      = "guess"       // the one of origin/main and origin/master that exists
)

// Repo is one clone.
type Repo struct {
	Path string // the main worktree
	Name string // the directory name

	OriginURL string
	Host      string // github.com, from the origin URL
	Slug      string // owner/name from the origin URL; empty without a GitHub origin

	gitx.Status

	// Default is the default branch BASE and cleanup measure against, and
	// DefaultSource where it came from; both empty when it is unknown.
	Default       string
	DefaultSource string
	// OriginHEAD is what refs/remotes/origin/HEAD points to, kept to say
	// when GitHub disagrees.
	OriginHEAD string
	// DefaultOid is the commit origin/<Default> is at locally; GitHub being
	// at another one means the counts are out of date.
	DefaultOid string
	// BaseBehind is how many commits the default branch has that HEAD does
	// not: the larger of origin/<Default> and the local <Default>. Zero on
	// the default branch itself, detached, or with the default unknown.
	BaseBehind int

	Worktrees []Worktree
	Branches  []gitx.Branch // every local branch, the current one included

	FetchedAt   time.Time // zero when never fetched
	LastCommit  time.Time
	LastSubject string

	Err error // reading it failed; the other fields are what was read
}

// Worktree is a linked or main worktree with its own state.
type Worktree struct {
	gitx.Worktree
	Main  bool
	Dirty int // changed and untracked files; -1 when not read (prunable)
	// Ahead is how many commits the worktree's branch has that the default
	// branch does not, and Behind the other way; Measured says they were
	// counted (a branch, and the default branch known).
	Ahead, Behind int
	Measured      bool
}

// Linked returns the worktrees other than the main one.
func (r *Repo) Linked() []Worktree {
	var w []Worktree
	for _, x := range r.Worktrees {
		if !x.Main {
			w = append(w, x)
		}
	}
	return w
}

// HasOrigin reports whether the clone has a remote called origin.
func (r *Repo) HasOrigin() bool { return r.OriginURL != "" }

// Dirty is the number of staged and unstaged changes.
func (r *Repo) Dirty() int { return r.Staged + r.Unstaged + r.Conflicts }

// Other branches by what they have to say.
func (r *Repo) Unpushed() []gitx.Branch {
	return r.others(func(b gitx.Branch) bool { return b.Upstream != "" && !b.Gone && b.Ahead > 0 })
}

// NoUpstream returns the local branches, other than the current one, that
// track nothing.
func (r *Repo) NoUpstream() []gitx.Branch {
	return r.others(func(b gitx.Branch) bool { return b.Upstream == "" })
}

// GoneUpstream returns the branches whose upstream was deleted.
func (r *Repo) GoneUpstream() []gitx.Branch {
	return r.others(func(b gitx.Branch) bool { return b.Gone })
}

func (r *Repo) others(keep func(gitx.Branch) bool) []gitx.Branch {
	var out []gitx.Branch
	for _, b := range r.Branches {
		if b.Name != r.Head && keep(b) {
			out = append(out, b)
		}
	}
	return out
}

// Score ranks how much a repository has to say, highest first: work that
// exists nowhere else weighs most.
func (r *Repo) Score() int {
	s := 0
	add := func(cond bool, n int) {
		if cond {
			s += n
		}
	}
	add(!r.HasOrigin(), 100)
	add(r.Ahead > 0, 50)
	add(len(r.Unpushed()) > 0, 30)
	add(r.Dirty() > 0, 20)
	add(r.Stash > 0, 10)
	add(r.Behind > 0, 8)
	add(r.BaseBehind > 0, 8)
	add(len(r.Worktrees) > 1, 6)
	add(len(r.GoneUpstream()) > 0, 2)
	add(r.Err != nil, 200)
	return s
}

// Quiet reports whether there is nothing to say at all.
func (r *Repo) Quiet() bool { return r.Score() == 0 && r.Untracked == 0 }

// UntrackedOnly reports whether untracked files are all there is to say.
func (r *Repo) UntrackedOnly() bool { return r.Score() == 0 && r.Untracked > 0 }

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

// Scan reads the clone at path.
func Scan(ctx context.Context, path string) *Repo {
	r := &Repo{Path: path, Name: filepath.Base(path)}
	git := func(args ...string) (string, error) { return gitx.Run(ctx, path, args...) }

	out, err := git("status", "--porcelain=v2", "--branch", "--show-stash", "-z")
	if err != nil {
		r.Err = err
		return r
	}
	r.Status = gitx.ParseStatus(out)

	if u, err := git("config", "--get", "remote.origin.url"); err == nil {
		r.OriginURL = u
		if h, s, ok := gitx.Slug(u); ok {
			r.Host, r.Slug = h, s
		}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if out, err := git("symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
			r.OriginHEAD = strings.TrimPrefix(out, "origin/")
		}
	})
	wg.Go(func() {
		if out, err := git("for-each-ref", "--format="+gitx.BranchFormat, "refs/heads"); err == nil {
			r.Branches = gitx.ParseBranches(out)
		}
	})
	wg.Go(func() {
		if out, err := git("log", "-1", "--format=%ct%x00%s"); err == nil {
			if ts, subj, ok := strings.Cut(out, "\x00"); ok {
				if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
					r.LastCommit = time.Unix(n, 0)
				}
				r.LastSubject = subj
			}
		}
	})
	wg.Go(func() {
		if common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			if fi, err := os.Stat(filepath.Join(common, "FETCH_HEAD")); err == nil {
				r.FetchedAt = fi.ModTime()
			}
		}
	})
	wg.Go(func() {
		out, err := git("worktree", "list", "--porcelain", "-z")
		if err != nil {
			return
		}
		for i, w := range gitx.ParseWorktrees(out) {
			wt := Worktree{Worktree: w, Main: i == 0, Dirty: -1}
			if i > 0 && !w.Prunable {
				if s, err := gitx.Run(ctx, w.Path, "status", "--porcelain", "-z"); err == nil {
					wt.Dirty = strings.Count(s, "\x00")
				}
			}
			r.Worktrees = append(r.Worktrees, wt)
		}
	})
	wg.Wait()

	if r.OriginHEAD != "" {
		r.Default, r.DefaultSource = r.OriginHEAD, DefaultFromOriginHEAD
	} else {
		var found []string
		for _, b := range []string{"main", "master"} {
			if _, err := git("rev-parse", "-q", "--verify", "refs/remotes/origin/"+b); err == nil {
				found = append(found, b)
			}
		}
		if len(found) == 1 {
			r.Default, r.DefaultSource = found[0], DefaultFromGuess
		}
	}
	r.measureBase(ctx)
	return r
}

// SetDefault makes branch, GitHub's default, the one BASE measures against,
// and measures it again when that changes it.
func (r *Repo) SetDefault(ctx context.Context, branch string) {
	if branch == "" || (branch == r.Default && r.DefaultSource == DefaultFromGitHub) {
		return
	}
	changed := branch != r.Default
	r.Default, r.DefaultSource = branch, DefaultFromGitHub
	if changed {
		r.measureBase(ctx)
	}
}

func (r *Repo) measureBase(ctx context.Context) {
	r.BaseBehind, r.DefaultOid = 0, ""
	if r.Default == "" {
		return
	}
	if oid, err := gitx.Run(ctx, r.Path, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+r.Default); err == nil {
		r.DefaultOid = oid
	}
	r.measureWorktrees(ctx)
	if r.Head == "" || r.Head == r.Default || r.Oid == "" {
		return
	}
	for _, ref := range []string{"refs/remotes/origin/" + r.Default, "refs/heads/" + r.Default} {
		out, err := gitx.Run(ctx, r.Path, "rev-list", "--count", "HEAD.."+ref)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(out); err == nil && n > r.BaseBehind {
			r.BaseBehind = n
		}
	}
}

// measureWorktrees counts each linked worktree's branch against the
// default branch: origin/<Default>, or the local one without it. It writes a
// new slice, since a copy of r made to measure in the background shares the
// old one with the list drawing it.
func (r *Repo) measureWorktrees(ctx context.Context) {
	base := "refs/remotes/origin/" + r.Default
	if r.DefaultOid == "" {
		base = "refs/heads/" + r.Default
	}
	wts := slices.Clone(r.Worktrees)
	for i, w := range wts {
		wts[i].Ahead, wts[i].Behind, wts[i].Measured = 0, 0, false
		if w.Main || w.Branch == "" || w.Prunable {
			continue
		}
		out, err := gitx.Run(ctx, r.Path, "rev-list", "--left-right", "--count", base+"...refs/heads/"+w.Branch)
		if err != nil {
			continue
		}
		var behind, ahead int
		if _, err := fmt.Sscanf(out, "%d\t%d", &behind, &ahead); err == nil {
			wts[i].Ahead, wts[i].Behind, wts[i].Measured = ahead, behind, true
		}
	}
	r.Worktrees = wts
}

// IsStale reports whether GitHub's default branch, at remoteOid, has moved
// past what this clone last fetched, so the counts are out of date.
func (r *Repo) IsStale(remoteOid string) bool {
	return remoteOid != "" && r.HasOrigin() && r.DefaultOid != remoteOid
}

// ScanAll scans paths with at most n at a time, sending each repository as
// soon as it is read.
func ScanAll(ctx context.Context, paths []string, n int, each func(*Repo)) {
	sem := make(chan struct{}, max(1, n))
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			each(Scan(ctx, p))
		})
	}
	wg.Wait()
}

// SortByName sorts repositories by directory name.
func SortByName(rs []*Repo) {
	slices.SortFunc(rs, func(a, b *Repo) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
}

// PullBlocker says why git pull --ff-only should not run, or "" when it
// may: it would refuse, or the result would surprise.
func (r *Repo) PullBlocker() string {
	switch {
	case r.Err != nil:
		return "unreadable"
	case !r.HasOrigin():
		return "no origin to pull from"
	case r.Head == "":
		return "detached HEAD"
	case r.Upstream == "":
		return "no upstream"
	case r.Dirty() > 0:
		return fmt.Sprintf("%d uncommitted changes", r.Dirty())
	case r.Ahead > 0 && r.Behind > 0:
		return "diverged from " + r.Upstream + "; rebase or merge it yourself"
	}
	return ""
}

// CanFastForward reports whether pull --ff-only would move the branch with
// nothing in the way: behind, not ahead, nothing uncommitted.
func (r *Repo) CanFastForward() bool {
	return r.PullBlocker() == "" && r.Behind > 0 && r.Ahead == 0
}
