package columns

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
)

// Unread is the mark of a value not read yet. It does not move: a spinner
// in every cell would draw the eye to what is not known.
var Unread = Cell{{Text: "⋅", Role: Dim}}

// sym writes a mark with its count when the symbols say to write counts.
func (x Ctx) sym(mark string, n int) string {
	if x.Symbols.Counts {
		return mark + strconv.Itoa(n)
	}
	return mark
}

// diverged writes behind and ahead together: <2>1 with counts, <> without.
func (x Ctx) diverged(behind, ahead int) string {
	if x.Symbols.Counts {
		return x.Symbols.Behind + strconv.Itoa(behind) + x.Symbols.Ahead + strconv.Itoa(ahead)
	}
	return x.Symbols.Diverged
}

func repoCell(x Ctx, it Item) Cell {
	r := it.Row
	switch it.Kind {
	case KindWorktree:
		w := it.Tree
		role := Worktree
		if w.Prunable {
			role = Dim
		}
		name := w.Holds()
		if name == "" {
			name = "(detached)"
		}
		return Cell{{Text: x.Symbols.Worktree + " " + name, Role: role}}
	case KindRemote:
		c := Cell{{Text: remoteName(r), Role: Muted}}
		return append(c, listedMarks(r)...)
	}
	var c Cell
	if x.Owner != nil && x.Owner.All {
		if o := ownerOf(r.Origin); o != "" {
			c = append(c, Seg{Text: o + "/", Role: Dim})
		}
	}
	name := ""
	if r.Local.Has {
		name = r.Local.Value.Name
	}
	if name == "" {
		name = remoteName(r)
	}
	c = append(c, Seg{Text: name, Role: Title, Bold: true})
	if r.Clone != "" && x.Twins[twin(r.Clone)] {
		if label := RootLabel(x.Roots, string(r.Clone)); label != "" {
			c = append(c, Seg{Text: " " + label, Role: Dim})
		}
	}
	if !r.Local.Has {
		return c
	}
	cl := r.Local.Value
	if n := len(cl.Linked()); n > 0 {
		c = append(c, Seg{Text: " " + x.Symbols.Worktree + strconv.Itoa(n), Role: Worktree})
	}
	switch {
	case !cl.HasOrigin():
		c = append(c, Seg{Text: " local only", Role: Warn})
	case r.Origin == "":
		c = append(c, Seg{Text: " not on GitHub", Role: Muted})
	case x.Owner != nil && !x.Owner.All && !strings.EqualFold(ownerOf(r.Origin), x.Owner.Login):
		c = append(c, Seg{Text: " → " + slugOf(r.Origin), Role: Warn})
	}
	return append(c, listedMarks(r)...)
}

func listedMarks(r *model.Row) Cell {
	var c Cell
	if r.Listed.Has && r.Listed.Value.Fork {
		c = append(c, Seg{Text: " fork", Role: Dim})
	}
	if r.Listed.Has && r.Listed.Value.Archived {
		c = append(c, Seg{Text: " archived", Role: Dim})
	}
	if r.Heavy.Has && !r.Heavy.Value.Found && r.Heavy.Last == model.OK {
		c = append(c, Seg{Text: " not found", Role: Warn})
	}
	return c
}

// remoteName is the repository's name on GitHub: the list's, or the last
// part of its key.
func remoteName(r *model.Row) string {
	if r.Listed.Has && r.Listed.Value.Name != "" {
		return r.Listed.Value.Name
	}
	k := string(r.Remote)
	if k == "" {
		k = string(r.Origin)
	}
	return k[strings.LastIndex(k, "/")+1:]
}

// ownerOf and slugOf read a remote key, host/owner/name.
func ownerOf(k model.RemoteKey) string {
	parts := strings.Split(string(k), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[1]
}

func slugOf(k model.RemoteKey) string {
	if _, slug, ok := strings.Cut(string(k), "/"); ok {
		return slug
	}
	return string(k)
}

// opCell writes what is being done to the clone or what was done, when
// there is.
func opCell(r *model.Row) (Cell, bool) {
	switch {
	case r.Op.Doing != "":
		return Cell{{Text: r.Op.Doing, Role: Accent}}, true
	case r.Op.Result == "":
		return nil, false
	case r.Op.Failed:
		return Cell{{Text: r.Op.Result, Role: Warn}}, true
	case r.Op.Skipped:
		return Cell{{Text: r.Op.Result, Role: Prompt}}, true
	}
	return Cell{{Text: r.Op.Result, Role: OK}}, true
}

func mainCell(x Ctx, it Item) Cell {
	r := it.Row
	if it.Kind == KindWorktree {
		return nil
	}
	if c, ok := opCell(r); ok {
		return c
	}
	if it.Kind == KindRemote {
		return Cell{{Text: "not cloned", Role: Dim}}
	}
	s := Main(r)
	var newer Cell
	if s.GitHubAhead {
		newer = Cell{{Text: x.Symbols.GitHubAhead, Role: Prompt}}
	}
	switch s.Kind {
	case MainUnknown:
		return Unread
	case MainNoOrigin:
		return Cell{{Text: "no origin", Role: Dim}}
	case MainNoDefault:
		return Cell{{Text: "default unknown", Role: Muted}}
	case MainNoLocal:
		return Cell{{Text: "no " + s.Name, Role: Muted}}
	case MainOtherUpstream:
		up := s.Upstream
		if up == "" {
			up = "nothing"
		}
		return Cell{{Text: "tracks " + up, Role: Muted}}
	case MainDiverged:
		return append(Cell{{Text: x.diverged(s.Behind, s.Ahead), Role: Warn}}, newer...)
	case MainAhead:
		return append(Cell{{Text: x.sym(x.Symbols.Ahead, s.Ahead), Role: Warn}}, newer...)
	case MainBehind:
		return append(Cell{{Text: x.sym(x.Symbols.Behind, s.Behind), Role: Accent}}, newer...)
	}
	if s.GitHubAhead {
		return Cell{{Text: x.Symbols.Behind, Role: Accent}, {Text: x.Symbols.GitHubAhead, Role: Prompt}}
	}
	return Cell{{Text: x.Symbols.Synced, Role: Dim}}
}

// upstream writes where a branch stands against its upstream.
func (x Ctx) upstream(b model.Branch) Seg {
	switch {
	case b.Gone:
		return Seg{Text: "upstream gone", Role: Warn}
	case b.Upstream == "":
		return Seg{Text: "no upstream", Role: Muted}
	case b.Ahead > 0 && b.Behind > 0:
		return Seg{Text: x.diverged(b.Behind, b.Ahead), Role: Warn}
	case b.Ahead > 0:
		return Seg{Text: x.sym(x.Symbols.Ahead, b.Ahead), Role: Worktree}
	case b.Behind > 0:
		return Seg{Text: x.sym(x.Symbols.Behind, b.Behind), Role: Accent}
	}
	return Seg{Text: x.Symbols.Synced, Role: Dim}
}

func branchOf(c model.Clone, name string) (model.Branch, bool) {
	for _, b := range c.Branches {
		if b.Name == name {
			return b, true
		}
	}
	return model.Branch{}, false
}

func checkedOutCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind == KindRemote:
		return nil
	case !r.Local.Has:
		return Unread
	}
	c := r.Local.Value
	head, behindMain := c.Head, c.BaseBehind
	if it.Kind == KindWorktree {
		head, behindMain = it.Tree.Holds(), 0
		if it.Tree.Measured {
			behindMain = it.Tree.BehindMain
		}
		if it.Tree.Prunable {
			return Cell{{Text: head, Role: Dim}}
		}
	}
	switch {
	case head == "":
		return Cell{{Text: "detached", Role: Muted}}
	case head == c.Default && it.Kind == KindClone:
		return Cell{{Text: head, Role: Dim}}
	}
	out := Cell{{Text: head + " ", Role: Strong}}
	if b, ok := branchOf(c, head); ok {
		out = append(out, x.upstream(b))
	}
	if behindMain > 0 {
		out = append(out, Seg{Text: " " + x.sym(x.Symbols.BehindMain, behindMain), Role: Accent})
	}
	return out
}

func changesCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind == KindRemote:
		return nil
	case !r.Local.Has:
		return Unread
	case it.Kind == KindWorktree:
		if it.Tree.Dirty > 0 {
			return Cell{{Text: x.sym(x.Symbols.Unstaged, it.Tree.Dirty), Role: Prompt}}
		}
		return nil
	}
	return x.changes(r.Local.Value.Status)
}

// changes writes a clone's changes as its prompt would.
func (x Ctx) changes(s model.Status) Cell {
	var c Cell
	add := func(mark string, n int, role Role) {
		if n == 0 {
			return
		}
		if len(c) > 0 && x.Symbols.Counts {
			c = append(c, Seg{Text: " "})
		}
		c = append(c, Seg{Text: x.sym(mark, n), Role: role})
	}
	add(x.Symbols.Unstaged, s.Unstaged+s.Conflicts, Prompt)
	add(x.Symbols.Staged, s.Staged, OK)
	add(x.Symbols.Untracked, s.Untracked, Muted)
	add(x.Symbols.Stash, s.Stash, Title)
	return c
}

// verdictRole is the color of a verdict.
func verdictRole(v policy.Verdict) Role {
	switch v {
	case policy.VerdictBusy:
		return Worktree
	case policy.VerdictNew:
		return Accent
	case policy.VerdictDone:
		return OK
	}
	return Dim
}

func worktreesCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind == KindRemote:
		return nil
	case !r.Local.Has:
		return Unread
	case it.Kind == KindWorktree:
		return x.worktreeVerdict(r, *it.Tree)
	}
	linked := r.Local.Value.Linked()
	var c Cell
	if len(linked) > 0 {
		counts := map[policy.Verdict]int{}
		unread := 0
		for _, w := range linked {
			v, _, ok := Verdict(r, w, x.Now)
			if !ok {
				unread++
				continue
			}
			if v == policy.VerdictGone {
				v = policy.VerdictDone
			}
			counts[v]++
		}
		for _, v := range []policy.Verdict{policy.VerdictBusy, policy.VerdictNew, policy.VerdictDone, policy.VerdictIdle} {
			if counts[v] > 0 {
				c = append(c, Seg{Text: fmt.Sprintf("%d %s  ", counts[v], v), Role: verdictRole(v)})
			}
		}
		if unread > 0 {
			c = append(c, Seg{Text: strconv.Itoa(unread) + " ", Role: Dim}, Seg{Text: "⋅  ", Role: Dim})
		}
	}
	if n := len(LooseBranches(r)); n > 0 {
		lead := ""
		if len(c) > 0 {
			lead = "· "
		}
		c = append(c, Seg{Text: fmt.Sprintf("%s+%d br", lead, n), Role: Muted})
	}
	return c
}

// worktreeVerdict writes a worktree's verdict, with what makes it busy
// and how long ago it counts from.
func (x Ctx) worktreeVerdict(r *model.Row, w model.Worktree) Cell {
	v, since, ok := Verdict(r, w, x.Now)
	if !ok {
		return Unread
	}
	text, role := string(v), verdictRole(v)
	switch v {
	case policy.VerdictGone:
		text = "gone · prune"
	case policy.VerdictBusy:
		b, _ := branchOf(r.Local.Value, w.Holds())
		switch {
		case w.Dirty > 0:
			text, role = "changes", Prompt
		case w.InProgress != "":
			text, role = w.InProgress+" stopped", Warn
		case b.Ahead > 0:
			text = fmt.Sprintf("%d unpushed", b.Ahead)
		default:
			text, role = "active", Muted
		}
	}
	c := Cell{{Text: text, Role: role}}
	if a := Age(since, x.Now); a != "" {
		c = append(c, Seg{Text: " " + a, Role: Muted})
	}
	return c
}

func fetchedCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind != KindClone:
		return nil
	case !r.Local.Has:
		return Unread
	case !r.Local.Value.HasOrigin():
		return Cell{{Text: "-", Role: Dim}}
	}
	role := Muted
	if GitHubAhead(r) {
		role = Prompt
	}
	return Cell{{Text: Ago(r.Local.Value.FetchedAt, x.Now), Role: role}}
}

// heavy returns the row's fields read from GitHub, or the cell to write
// instead: - without a repository, n/a when GitHub does not give them,
// the unread mark while they are not read.
func heavy(it Item, need model.Need) (model.Heavy, Cell, bool) {
	r := it.Row
	switch {
	case it.Kind == KindWorktree:
		return model.Heavy{}, nil, false
	case r.Remote == "":
		return model.Heavy{}, Cell{{Text: "-", Role: Dim}}, false
	case !r.Heavy.Has && r.Heavy.Last == model.Denied:
		return model.Heavy{}, Cell{{Text: "n/a", Role: Dim}}, false
	case !r.Heavy.Has || (need != 0 && !r.Heavy.Value.Fields.Has(need)):
		return model.Heavy{}, Unread, false
	case !r.Heavy.Value.Found:
		return model.Heavy{}, Cell{{Text: "-", Role: Dim}}, false
	}
	return r.Heavy.Value, nil, true
}

func count(n int) Cell {
	if n == 0 {
		return Cell{{Text: "0", Role: Dim}}
	}
	return Cell{{Text: strconv.Itoa(n), Role: Text}}
}

func issuesCell(_ Ctx, it Item) Cell {
	h, c, ok := heavy(it, model.NeedCounts)
	if !ok {
		return c
	}
	return count(h.Issues)
}

func prsCell(_ Ctx, it Item) Cell {
	h, c, ok := heavy(it, model.NeedCounts)
	if !ok {
		return c
	}
	return count(h.PRs)
}

func dependabotCell(_ Ctx, it Item) Cell {
	h, c, ok := heavy(it, model.NeedAlerts)
	switch {
	case !ok:
		return c
	case h.AlertsDenied:
		return Cell{{Text: "n/a", Role: Dim}}
	case h.AlertsOff:
		return Cell{{Text: "off", Role: Muted}}
	case h.Alerts > 0:
		return Cell{{Text: strconv.Itoa(h.Alerts), Role: Warn}}
	}
	return Cell{{Text: "0", Role: Dim}}
}

func ciCell(_ Ctx, it Item) Cell {
	h, c, ok := heavy(it, model.NeedCI)
	if !ok {
		return c
	}
	switch h.CI {
	case "SUCCESS":
		return Cell{{Text: "✓", Role: OK}}
	case "FAILURE", "ERROR":
		return Cell{{Text: "✗", Role: Warn}}
	case "PENDING", "EXPECTED":
		return Cell{{Text: "~", Role: Prompt}}
	}
	return Cell{{Text: "-", Role: Dim}}
}

func pushedCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind == KindWorktree:
		return nil
	case r.Remote == "":
		return Cell{{Text: "-", Role: Dim}}
	case r.Listed.Has:
		return Cell{{Text: Ago(r.Listed.Value.PushedAt, x.Now), Role: Subtle}}
	case r.Heavy.Has && r.Heavy.Value.Found:
		return Cell{{Text: Ago(r.Heavy.Value.PushedAt, x.Now), Role: Subtle}}
	case r.Heavy.Last == model.Denied:
		return Cell{{Text: "n/a", Role: Dim}}
	}
	return Unread
}

// starsCell counts the repository's stars, from the list or, for an
// owner whose list is not read, from the fields asked by name.
func starsCell(x Ctx, it Item) Cell {
	r := it.Row
	switch {
	case it.Kind == KindWorktree:
		return nil
	case r.Remote == "":
		return Cell{{Text: "-", Role: Dim}}
	case r.Listed.Has:
		return stars(r.Listed.Value.Stars)
	case r.Heavy.Has && r.Heavy.Value.Found:
		return stars(r.Heavy.Value.Stars)
	case r.Heavy.Last == model.Denied:
		return Cell{{Text: "n/a", Role: Dim}}
	}
	return Unread
}

// stars writes a count of stars, in thousands from 10,000 on, so that it
// fits the column.
func stars(n int) Cell {
	switch {
	case n == 0:
		return Cell{{Text: "0", Role: Dim}}
	case n >= 10000:
		return Cell{{Text: strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k", Role: Subtle}}
	}
	return Cell{{Text: strconv.Itoa(n), Role: Subtle}}
}

func visibilityCell(_ Ctx, it Item) Cell {
	r := it.Row
	if it.Kind != KindWorktree && r.Listed.Has && r.Listed.Value.Private {
		return Cell{{Text: "private", Role: Dim}}
	}
	return nil
}
