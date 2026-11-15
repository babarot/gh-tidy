package columns

import (
	"fmt"
	"slices"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
)

// MainKind is where a clone's default branch stands against origin's.
type MainKind int

const (
	MainUnknown       MainKind = iota // the clone is not read yet
	MainNoOrigin                      // no origin to compare with
	MainNoDefault                     // the default branch is not known
	MainNoLocal                       // no local default branch
	MainOtherUpstream                 // the local default branch tracks something else
	MainCurrent                       // as origin's, as far as git knows
	MainBehind                        // origin's has commits the local one lacks
	MainAhead                         // the local one has commits origin's lacks
	MainDiverged                      // both
)

// MainState is where a clone's default branch stands, and whether p would
// move it.
type MainState struct {
	Kind          MainKind
	Name          string // the default branch
	Behind, Ahead int
	// GitHubAhead says GitHub's default branch has commits the clone has
	// not fetched.
	GitHubAhead bool
	// Upstream is what the local default branch tracks, for
	// MainOtherUpstream.
	Upstream string
	// Blocked says why p would leave a default branch behind origin's;
	// empty when it would move it, or when there is nothing to move.
	Blocked string
	// CheckedOut says the default branch is checked out in a worktree, so
	// p merges there instead of moving the ref.
	CheckedOut bool
}

// Main tells where the row's clone's default branch stands.
func Main(r *model.Row) MainState {
	if !r.Local.Has {
		return MainState{}
	}
	c := r.Local.Value
	s := MainState{Name: c.Default, GitHubAhead: GitHubAhead(r)}
	switch {
	case !c.HasOrigin():
		s.Kind = MainNoOrigin
		return s
	case c.Default == "":
		s.Kind = MainNoDefault
		return s
	}
	i := slices.IndexFunc(c.Branches, func(b model.Branch) bool { return b.Name == c.Default })
	if i < 0 {
		s.Kind = MainNoLocal
		return s
	}
	b := c.Branches[i]
	if b.Upstream != "origin/"+c.Default {
		s.Kind, s.Upstream = MainOtherUpstream, b.Upstream
		return s
	}
	s.Behind, s.Ahead = b.Behind, b.Ahead
	switch {
	case b.Ahead > 0 && b.Behind > 0:
		s.Kind = MainDiverged
	case b.Ahead > 0:
		s.Kind = MainAhead
	case b.Behind > 0:
		s.Kind = MainBehind
	default:
		s.Kind = MainCurrent
	}
	for _, w := range c.Worktrees {
		if w.Holds() != c.Default {
			continue
		}
		s.CheckedOut = true
		switch {
		case w.Prunable:
			s.Blocked = "held by a worktree that is gone"
		case w.InProgress != "":
			s.Blocked = fmt.Sprintf("a %s is stopped half way on %s", w.InProgress, c.Default)
		case w.Dirty > 0:
			s.Blocked = fmt.Sprintf("%d uncommitted change%s on %s", w.Dirty, plural(w.Dirty), c.Default)
		}
	}
	if !c.DefaultSource.Trusted() {
		s.Blocked = c.Default + " is a guess: run git remote set-head origin -a"
	}
	return s
}

// CanUpdate reports whether p would move the default branch, as far as
// what is read tells: p fetches and decides again.
func (s MainState) CanUpdate() bool {
	return s.Blocked == "" && (s.Kind == MainBehind || (s.Kind == MainCurrent && s.GitHubAhead))
}

// GitHubAhead reports whether GitHub's default branch has commits the
// clone has not fetched, decided for the commit origin is at now.
func GitHubAhead(r *model.Row) bool {
	return r.Local.Has && r.Ahead.Has && r.Ahead.Value.Ahead && r.Ahead.Value.Local == r.Local.Value.DefaultOid
}

// Verdict judges a linked worktree of the row's clone; ok is false while
// what judging it needs is not read.
func Verdict(r *model.Row, w model.Worktree, now time.Time) (v policy.Verdict, since time.Time, ok bool) {
	if w.Prunable {
		return policy.VerdictGone, time.Time{}, true
	}
	if !r.Local.Has || !r.Facts.Has {
		return "", time.Time{}, false
	}
	v, since = policy.Judge(r.Local.Value, w, r.Facts.Value, now)
	return v, since, true
}

// Removes reports whether d removes a worktree judged v.
func Removes(v policy.Verdict) bool { return v == policy.VerdictDone || v == policy.VerdictGone }

// Loose is a local branch in no worktree that holds work found nowhere
// else, which d leaves.
type Loose struct {
	Name string
	// Gone says its upstream was deleted; otherwise it never had one.
	Gone bool
}

// LooseBranches are the clone's branches in no worktree, other than the
// default, never pushed or with their upstream deleted, and not merged.
func LooseBranches(r *model.Row) []Loose {
	if !r.Local.Has {
		return nil
	}
	c := r.Local.Value
	held := map[string]bool{}
	for _, w := range c.Worktrees {
		held[w.Holds()] = true
	}
	var out []Loose
	for _, b := range c.Branches {
		if b.Name == c.Default || held[b.Name] || (b.Upstream != "" && !b.Gone) {
			continue
		}
		if r.Facts.Has && r.Facts.Value.Merged[b.Name] {
			continue
		}
		out = append(out, Loose{Name: b.Name, Gone: b.Gone})
	}
	return out
}

// Ago writes how long ago t was: "now", "5m ago", "3h ago", "2d ago", and
// the date after a month.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
	return t.Local().Format("Jan 2")
}

// Age writes how long ago t was, short: "5m", "3h", "2d"; empty when not
// known.
func Age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d/time.Minute)))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
