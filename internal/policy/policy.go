// Package policy decides what gh-tidy does with a clone, from facts read
// before: whether its default branch may be moved forward and how, what d
// removes and keeps, how a worktree is judged, and how much a clone needs
// attention. It reads nothing and changes nothing; its rules keep the
// user's work safe, so they are tested here without git.
package policy

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

// How a default branch is moved forward.
type AdvanceAction int

const (
	// AdvanceSkip leaves the branch as it is; Why says why.
	AdvanceSkip AdvanceAction = iota
	// AdvanceCurrent leaves it: it is already at origin's.
	AdvanceCurrent
	// AdvanceRef moves the branch ref, checked out nowhere.
	AdvanceRef
	// AdvanceMerge fast-forwards the worktree the branch is checked out in.
	AdvanceMerge
)

// Advance is the decision about a clone's default branch.
type Advance struct {
	Action AdvanceAction
	// Worktree is where AdvanceMerge runs.
	Worktree string
	// Commits is how many commits it brings in.
	Commits int
	Why     string
}

// DecideAdvance decides whether, and how, to move a clone's default branch
// forward to origin's, from facts read right before. Only a fast-forward
// is ever made, only of a branch whose name came from GitHub or
// origin/HEAD and which tracks origin, and only in a worktree with nothing
// in the way.
func DecideAdvance(f model.AdvanceFacts) Advance {
	skip := func(why string) Advance { return Advance{Action: AdvanceSkip, Why: why} }
	switch {
	case f.Default == "":
		return skip("default branch unknown")
	case !f.DefaultSource.Trusted():
		return skip("default branch only guessed; run git remote set-head origin -a")
	case !f.RemoteExists:
		return skip("origin/" + f.Default + " does not exist")
	case !f.LocalExists:
		return skip("no local " + f.Default + " branch")
	case f.Upstream != "origin/"+f.Default:
		up := f.Upstream
		if up == "" {
			up = "nothing"
		}
		return skip(f.Default + " tracks " + up + ", not origin/" + f.Default)
	case f.Behind == 0:
		return Advance{Action: AdvanceCurrent, Why: "up to date"}
	case !f.FastForward:
		return skip(f.Default + " has commits origin/" + f.Default + " does not; rebase or merge it yourself")
	case f.CheckedOut == "":
		return Advance{Action: AdvanceRef, Commits: f.Behind}
	case f.InProgress != "":
		return skip("a " + f.InProgress + " is in progress where " + f.Default + " is checked out")
	case f.Dirty != 0:
		if f.Dirty < 0 {
			return skip("the worktree " + f.Default + " is checked out in could not be read")
		}
		return skip(fmt.Sprintf("%d uncommitted change%s where %s is checked out", f.Dirty, plural(f.Dirty), f.Default))
	}
	return Advance{Action: AdvanceMerge, Worktree: f.CheckedOut, Commits: f.Behind}
}

// Cleanup is what the user may ask d not to touch, and whether branches
// whose upstream is gone but which are not merged go too.
type Cleanup struct {
	Protect    []string
	DeleteGone bool
}

// What an item of a cleanup plan is.
type ItemKind int

const (
	ItemWorktree ItemKind = iota // a linked worktree and its branch
	ItemPrunable                 // a worktree whose directory is gone
	ItemBranch                   // a branch checked out nowhere
)

// Item is one thing to remove or keep.
type Item struct {
	Kind   ItemKind
	Path   string // worktrees
	Branch string
	// Why is how it is removed, or why it is kept.
	Why string
	// Force deletes the branch whether or not it is merged: its upstream is
	// gone and the user's setting asks for it.
	Force bool
}

// Plan is what d would do to one clone.
type Plan struct {
	Base   string
	Remove []Item
	Keep   []Item
}

// DecideCleanup plans the cleanup of c from facts read right before. cwd
// is where gh tidy runs: the worktree holding it is kept. Nothing with
// changes, never committed to, locked, protected or not merged is removed,
// unless DeleteGone asks for branches whose upstream is gone.
func DecideCleanup(c model.Clone, f model.CleanupFacts, opts Cleanup, cwd string) Plan {
	p := Plan{Base: f.Base}
	protected := func(b string) bool { return b == c.Default || b == c.Head || slices.Contains(opts.Protect, b) }
	checkedOut := map[string]bool{}
	for _, w := range c.Worktrees {
		if w.Branch != "" {
			checkedOut[w.Branch] = true
		}
	}
	for _, w := range c.Worktrees {
		if w.Main {
			continue
		}
		item := Item{Kind: ItemWorktree, Path: w.Path, Branch: w.Branch}
		if w.Prunable {
			item.Kind, item.Why = ItemPrunable, "directory gone · worktree prune"
			p.Remove = append(p.Remove, item)
			continue
		}
		if w.Branch == "" || !f.Merged[w.Branch] {
			continue // work of its own, or detached: not d's to judge
		}
		keep := func(why string) { item.Why = why; p.Keep = append(p.Keep, item) }
		switch {
		case within(cwd, w.Path):
			keep("you are in it")
		case w.Locked:
			keep("locked")
		case protected(w.Branch):
			keep("protected branch")
		case w.InProgress != "":
			keep("a " + w.InProgress + " is in progress")
		case w.Dirty < 0:
			keep("unreadable")
		case w.Dirty > 0:
			keep(fmt.Sprintf("%d uncommitted change%s", w.Dirty, plural(w.Dirty)))
		case !f.WorkedOn[w.Branch]:
			keep("no commits since it was created; may be in use")
		default:
			item.Why = "merged · clean"
			p.Remove = append(p.Remove, item)
		}
	}
	for _, b := range c.Branches {
		if checkedOut[b.Name] || protected(b.Name) {
			continue
		}
		switch {
		case f.Merged[b.Name]:
			p.Remove = append(p.Remove, Item{Kind: ItemBranch, Branch: b.Name, Why: "merged"})
		case b.Gone && opts.DeleteGone:
			p.Remove = append(p.Remove, Item{Kind: ItemBranch, Branch: b.Name, Why: "upstream gone · branch -D", Force: true})
		case b.Gone:
			p.Keep = append(p.Keep, Item{Kind: ItemBranch, Branch: b.Name, Why: "upstream gone but not merged (squash merge?); delete_gone = true removes it"})
		}
	}
	return p
}

// StillRemoves keeps, of the items a confirmed plan removed, those a plan
// made again from facts read now still removes. What changed since the
// confirm (a worktree an agent started working in) is left alone.
func StillRemoves(confirmed, now Plan) []Item {
	var out []Item
	for _, it := range confirmed.Remove {
		if slices.ContainsFunc(now.Remove, func(n Item) bool {
			return n.Kind == it.Kind && n.Path == it.Path && n.Branch == it.Branch && n.Force == it.Force
		}) {
			out = append(out, it)
		}
	}
	return out
}

// WithoutForce leaves out the items that delete a branch not merged: what
// removing across many clones at once (D) never does.
func WithoutForce(items []Item) []Item {
	return slices.DeleteFunc(slices.Clone(items), func(it Item) bool { return it.Force })
}

// Verdict is how a linked worktree is judged.
type Verdict string

const (
	VerdictBusy Verdict = "busy" // changes, unpushed commits, or touched lately
	VerdictNew  Verdict = "new"  // never committed to: about to be used
	VerdictDone Verdict = "done" // merged and clean
	VerdictIdle Verdict = "idle" // clean, not merged, untouched for long
	VerdictGone Verdict = "gone" // its directory is gone
)

// idleAfter is how long a clean worktree may sit untouched before it reads
// as idle. A constant: it only sorts what is shown.
const idleAfter = 14 * 24 * time.Hour

// Judge judges a linked worktree of c from the cleanup facts of c. since
// tells what the age shown beside the verdict counts from: the worktree's
// creation for a new one, its last activity otherwise; zero when unknown.
// Only done and gone are what d removes. A worktree whose branch has
// commits that exist nowhere else is busy however long it was left.
func Judge(c model.Clone, w model.Worktree, f model.CleanupFacts, now time.Time) (v Verdict, since time.Time) {
	merged := w.Branch != "" && f.Merged[w.Branch]
	switch {
	case w.Prunable:
		return VerdictGone, time.Time{}
	case merged && !f.WorkedOn[w.Branch]:
		return VerdictNew, w.Created
	case merged && w.Dirty == 0 && w.InProgress == "":
		return VerdictDone, w.Active
	case w.Dirty != 0 || w.InProgress != "" || unpushed(c, w):
		return VerdictBusy, w.Active
	case !w.Active.IsZero() && now.Sub(w.Active) > idleAfter:
		return VerdictIdle, w.Active
	}
	return VerdictBusy, w.Active
}

// unpushed reports whether the worktree's branch has commits its upstream
// does not, or has commits of its own and no upstream at all.
func unpushed(c model.Clone, w model.Worktree) bool {
	i := slices.IndexFunc(c.Branches, func(b model.Branch) bool { return b.Name == w.Branch })
	if i < 0 {
		return false
	}
	b := c.Branches[i]
	return b.Ahead > 0 || (b.Upstream == "" && w.AheadMain > 0)
}

// Attention ranks how much a clone needs looking at, highest first: work
// that exists nowhere else weighs most. remoteAhead says GitHub's default
// branch has commits the clone has not fetched.
func Attention(c model.Clone, remoteAhead bool) int {
	s := 0
	add := func(cond bool, n int) {
		if cond {
			s += n
		}
	}
	unpushed := len(c.OtherBranches(func(b model.Branch) bool { return b.Upstream != "" && !b.Gone && b.Ahead > 0 }))
	gone := len(c.OtherBranches(func(b model.Branch) bool { return b.Gone }))
	add(c.Err != "", 200)
	add(!c.HasOrigin(), 100)
	add(c.Ahead > 0 || (c.HasOrigin() && c.Head != "" && c.Head != c.Default && c.Upstream == ""), 50)
	add(unpushed > 0, 30)
	add(c.Dirty() > 0, 20)
	add(slices.ContainsFunc(c.Linked(), func(w model.Worktree) bool { return w.Dirty > 0 }), 20)
	add(c.Stash > 0, 10)
	add(c.Behind > 0 || remoteAhead, 8)
	add(c.BaseBehind > 0, 8)
	add(len(c.Linked()) > 0, 6)
	add(gone > 0, 2)
	return s
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
