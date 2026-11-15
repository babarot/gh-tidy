// Package model holds the values the other packages pass to each other: a
// clone's state as read from git, the facts a decision is made from, and
// the keys rows are found by. It reaches nothing outside the process and
// holds no behavior beyond reading these values.
package model

import (
	"path/filepath"
	"strings"
	"time"
)

// CloneKey names a clone: the path of its main worktree, cleaned. Two
// clones of one repository are two keys.
type CloneKey string

// NewCloneKey is the key of the clone whose main worktree is at path.
func NewCloneKey(path string) CloneKey { return CloneKey(filepath.Clean(path)) }

// RemoteKey names a repository on a host: host/owner/name in lower case,
// as GitHub compares them.
type RemoteKey string

// NewRemoteKey is the key of owner/name (slug) on host.
func NewRemoteKey(host, slug string) RemoteKey {
	if host == "" || slug == "" {
		return ""
	}
	return RemoteKey(strings.ToLower(host + "/" + slug))
}

// Where a clone's default branch came from, most trusted first. Only a
// name from GitHub or origin/HEAD may be written to; a guess is shown but
// never acted on.
type DefaultSource string

const (
	DefaultFromGitHub     DefaultSource = "github"      // the repository's default branch on GitHub
	DefaultFromOriginHEAD DefaultSource = "origin/HEAD" // refs/remotes/origin/HEAD
	DefaultFromGuess      DefaultSource = "guess"       // the one of origin/main and origin/master that exists
)

// Trusted reports whether a default branch from s may be written to.
func (s DefaultSource) Trusted() bool {
	return s == DefaultFromGitHub || s == DefaultFromOriginHEAD
}

// Status is a worktree's branch and what is not committed, as git status
// says it.
type Status struct {
	Head      string // branch name; empty when detached
	Oid       string // HEAD's commit; empty in a repository with no commits
	Upstream  string // such as origin/main; empty without one
	Ahead     int
	Behind    int
	HasAB     bool // Ahead and Behind are known (the upstream exists)
	Staged    int
	Unstaged  int
	Untracked int
	Conflicts int
	Stash     int
}

// Dirty is the number of staged, unstaged and conflicted changes: what
// stands in the way of moving the branch.
func (s Status) Dirty() int { return s.Staged + s.Unstaged + s.Conflicts }

// Branch is one local branch.
type Branch struct {
	Name     string
	Upstream string // empty without one
	Ahead    int
	Behind   int
	Gone     bool // the upstream is configured but no longer exists
	Date     time.Time
}

// Worktree is the main worktree or a linked one.
type Worktree struct {
	Path     string
	Head     string // commit
	Branch   string // short name; empty when detached
	Main     bool
	Prunable bool // its directory is gone
	Locked   bool
	// Dirty is the number of changed and untracked files; -1 when not read
	// (a prunable worktree).
	Dirty int
	// Tracked is the number of changes to tracked files alone, which is
	// what keeps a fast-forward out; -1 when not read.
	Tracked int
	// InProgress is an operation stopped half way in it: "rebase", "merge",
	// "cherry-pick", "revert" or "bisect"; empty when none.
	InProgress string
	// Occupies is the branch a rebase or a bisect stopped in it will come
	// back to. git lists such a worktree as detached, but the branch is as
	// good as checked out there.
	Occupies string
	// AheadMain is how many commits the worktree's branch has that the
	// default branch does not, and BehindMain the other way; Measured says
	// they were counted.
	AheadMain, BehindMain int
	Measured              bool
	// Created is when the worktree was made and Active when a commit or a
	// checkout last happened in it, from its own HEAD log; zero when not
	// known. Only for linked worktrees.
	Created, Active time.Time
}

// Holds is the branch the worktree holds: checked out, or waiting for a
// rebase or a bisect to come back to it.
func (w Worktree) Holds() string {
	if w.Branch != "" {
		return w.Branch
	}
	return w.Occupies
}

// Clone is one clone's state, read from git alone.
type Clone struct {
	Key  CloneKey
	Path string // the main worktree
	Name string // the directory name

	OriginURL string
	Host      string    // from the origin URL
	Slug      string    // owner/name from the origin URL; empty without a GitHub-like origin
	Remote    RemoteKey // empty without one

	Status

	// Default is the default branch, DefaultSource where it came from, and
	// OriginHEAD what refs/remotes/origin/HEAD points to; all empty when
	// unknown.
	Default       string
	DefaultSource DefaultSource
	OriginHEAD    string
	// DefaultOid is the commit origin/<Default> is at here.
	DefaultOid string
	// BaseBehind is how many commits the default branch has that HEAD does
	// not: the larger of origin/<Default> and the local <Default>. Zero on
	// the default branch itself, detached, or with the default unknown.
	BaseBehind int

	Worktrees []Worktree // the main one first
	Branches  []Branch   // every local branch, the current one included

	FetchedAt   time.Time // zero when never fetched
	LastCommit  time.Time
	LastSubject string

	// Err is what failed while reading; the other fields are what was read
	// before it.
	Err string
}

// HasOrigin reports whether the clone has a remote called origin.
func (c Clone) HasOrigin() bool { return c.OriginURL != "" }

// Linked returns the worktrees other than the main one.
func (c Clone) Linked() []Worktree {
	var out []Worktree
	for _, w := range c.Worktrees {
		if !w.Main {
			out = append(out, w)
		}
	}
	return out
}

// OtherBranches returns the local branches other than the current one that
// keep says yes to.
func (c Clone) OtherBranches(keep func(Branch) bool) []Branch {
	var out []Branch
	for _, b := range c.Branches {
		if b.Name != c.Head && keep(b) {
			out = append(out, b)
		}
	}
	return out
}

// AdvanceFacts are what deciding whether to move a clone's default branch
// forward needs, read right before the decision.
type AdvanceFacts struct {
	Default       string
	DefaultSource DefaultSource
	// LocalExists says refs/heads/<Default> exists, and Upstream is what it
	// tracks (such as origin/main).
	LocalExists bool
	Upstream    string
	// RemoteExists says refs/remotes/origin/<Default> exists.
	RemoteExists bool
	// Behind is how many commits origin/<Default> has that the local
	// branch does not, and FastForward whether the local branch is an
	// ancestor of origin/<Default>.
	Behind      int
	FastForward bool
	// CheckedOut is the worktree that holds the default branch (checked
	// out, or waiting for a rebase or a bisect to come back to it); empty
	// when none. Dirty (changes to tracked files; -1 when unreadable) and
	// InProgress are that worktree's.
	CheckedOut string
	Dirty      int
	InProgress string
	// Holders is how many worktrees hold the default branch, and
	// HeldByMissing whether one of them is a worktree whose directory git
	// cannot find (moved, or on a disk not mounted): it may still be in use.
	Holders       int
	HeldByMissing bool
}

// CleanupFacts are what deciding what d removes from a clone needs, read
// right before the decision.
type CleanupFacts struct {
	// Base is the ref merged is measured against: origin/<default>, or the
	// local default branch without it.
	Base string
	// Merged lists the local branches Base has.
	Merged map[string]bool
	// WorkedOn says, of a branch, whether it got a commit after it was
	// created. A branch made for a worktree and never committed to sits on
	// the default branch and looks merged, though it may be in use.
	WorkedOn map[string]bool
}
