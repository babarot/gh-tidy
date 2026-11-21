package tui

import (
	"cmp"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
	"github.com/babarot/gh-tidy/internal/tui/columns"
)

// The sections that fold, by the name their heads are kept under.
const (
	headQuiet     = "quiet"      // clones with nothing to do
	headNotCloned = "not-cloned" // repositories not cloned
	headUnlisted  = "unlisted"   // an owner whose list is not read unasked
)

// line is one line of the list: a clone, one of its worktrees, a
// repository not cloned, or the head of a section that folds.
type line struct {
	kind columns.Kind
	row  *model.Row
	tree *model.Worktree
	last bool // the last worktree of its clone

	head  string // which section, for a head
	title string
	n     int // rows in the section; -1 when not counted
	note  string
}

// key is what the cursor stays on: the row's key, a worktree's path, or
// a head's name.
func (l line) key() string {
	switch l.kind {
	case columns.KindWorktree:
		return "wt:" + l.tree.Path
	case columns.KindHead:
		return "head:" + l.head
	}
	return l.row.Key()
}

func (l line) item() columns.Item {
	return columns.Item{Kind: l.kind, Row: l.row, Tree: l.tree}
}

// name is what the filter matches and the preview is titled with.
func (l line) name() string {
	switch l.kind {
	case columns.KindHead:
		return l.title
	case columns.KindWorktree:
		return l.tree.Holds()
	}
	if l.row.Local.Has && l.row.Local.Value.Name != "" {
		return l.row.Local.Value.Name
	}
	k := string(l.row.Remote)
	return k[strings.LastIndex(k, "/")+1:]
}

// matches reports whether the filter q matches the row's name or the
// branch checked out in it, ignoring case.
func matches(r *model.Row, name, q string) bool {
	if q == "" {
		return true
	}
	words := []string{name}
	if r.Local.Has {
		words = append(words, r.Local.Value.Head)
		for _, w := range r.Local.Value.Linked() {
			words = append(words, w.Holds())
		}
	}
	return strings.Contains(strings.ToLower(strings.Join(words, " ")), strings.ToLower(q))
}

// attention ranks a clone in the local set: what needs looking at first.
// A clone not read yet ranks with those that need it, so that it shows
// while it is read.
func attention(r *model.Row) int {
	if !r.Local.Has {
		return 1
	}
	return policy.Attention(r.Local.Value, columns.GitHubAhead(r))
}

// githubAttention ranks a repository in the github set: open Dependabot
// alerts, then checks failing on the default branch, then new issues and
// pull requests (only when they can be told), then open pull requests and
// issues.
func githubAttention(r *model.Row) int {
	if !r.Heavy.Has || !r.Heavy.Value.Found {
		return 0
	}
	h := r.Heavy.Value
	s := 0
	if h.Alerts > 0 {
		s += 1000 + h.Alerts
	}
	if h.CI == "FAILURE" || h.CI == "ERROR" {
		s += 500
	}
	if n := r.New; n.Known && n.Issues+n.PRs > 0 {
		s += 250
	}
	return s + min(h.PRs, 99)*2 + min(h.Issues, 99)
}

func pushedAt(r *model.Row) int64 {
	switch {
	case r.Listed.Has:
		return r.Listed.Value.PushedAt.Unix()
	case r.Heavy.Has:
		return r.Heavy.Value.PushedAt.Unix()
	}
	return 0
}

// notCloned reports whether a repository not cloned goes in the list:
// listed, and neither a fork nor archived.
func notCloned(r *model.Row) bool {
	return r.Clone == "" && r.Listed.Has && !r.Listed.Value.Fork && !r.Listed.Value.Archived
}

// groupHead is the name a section the user named is kept under.
func groupHead(g config.Group) string { return "section:" + g.Name }

// groupOf is the first section the user named that r's repository
// matches, or -1. It groups rows for the screen; it does not match clones
// to GitHub. A clone is matched by where it lies, <owner>/<directory>,
// known before it is read and not moved by a rename found; a repository
// not cloned by its name on GitHub.
func (m *Model) groupOf(r *model.Row) int {
	var owner, name string
	if r.Clone != "" {
		p := string(r.Clone)
		owner, name = filepath.Base(filepath.Dir(p)), filepath.Base(p)
	} else if parts := strings.SplitN(string(r.Remote), "/", 3); len(parts) == 3 {
		owner, name = parts[1], parts[2] // host/owner/name
	}
	name, slug := strings.ToLower(name), strings.ToLower(owner+"/"+name)
	for i, g := range m.groups {
		for _, p := range g.Repos {
			subject := name
			if strings.Contains(p, "/") {
				subject = slug
			}
			if ok, _ := path.Match(strings.ToLower(p), subject); ok {
				return i
			}
		}
	}
	return -1
}

// headNote says what a section holds that needs doing, so that a folded
// one hides nothing: in the local columns how many can be updated and
// how many have changes, in the github ones how many have alerts and
// failing checks.
func (m *Model) headNote(rs []*model.Row) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	if m.lens == columns.GitHub {
		alerts, failing := 0, 0
		for _, r := range rs {
			if h := r.Heavy; h.Has && h.Value.Found {
				if h.Value.Alerts > 0 {
					alerts++
				}
				if h.Value.CI == "FAILURE" || h.Value.CI == "ERROR" {
					failing++
				}
			}
		}
		add(alerts, "with alerts")
		add(failing, "failing")
	} else {
		up, changed := 0, 0
		for _, r := range rs {
			if !r.Local.Has {
				continue
			}
			if columns.Main(r).CanUpdate() {
				up++
			}
			c := r.Local.Value
			if c.Dirty()+c.Untracked > 0 || slices.ContainsFunc(c.Linked(), func(w model.Worktree) bool { return w.Dirty > 0 }) {
				changed++
			}
		}
		add(up, "can update")
		add(changed, "with changes")
	}
	return strings.Join(parts, " · ")
}

// buildLines lays out the owner's rows for the lens. order, when it holds
// a row, keeps it where it was, so that values arriving do not move rows
// under the cursor; rows it does not hold are placed by rank after them.
func (m *Model) buildLines() []line {
	o := &m.snap
	var clones, remotes []*model.Row
	// The rows of each section the user named, taken out first: a row in
	// one goes nowhere else.
	grouped := make([][]*model.Row, len(m.groups))
	for i := range o.Rows {
		r := &o.Rows[i]
		name := line{kind: columns.KindClone, row: r}.name()
		if !matches(r, name, m.filter) {
			continue
		}
		if r.Clone == "" && (!notCloned(r) || o.All) {
			continue
		}
		if g := m.groupOf(r); g >= 0 {
			grouped[g] = append(grouped[g], r)
			continue
		}
		if r.Clone != "" {
			clones = append(clones, r)
		} else {
			remotes = append(remotes, r)
		}
	}
	byName := func(a, b *model.Row) int {
		return cmp.Compare(line{kind: columns.KindClone, row: a}.name(), line{kind: columns.KindClone, row: b}.name())
	}
	filtering := m.filter != ""
	var out []line
	push := func(r *model.Row, kind columns.Kind) {
		out = append(out, line{kind: kind, row: r})
		if kind != columns.KindClone || m.lens != columns.Local || !m.expanded[string(r.Clone)] || !r.Local.Has {
			return
		}
		linked := r.Local.Value.Linked()
		for i := range linked {
			out = append(out, line{kind: columns.KindWorktree, row: r, tree: &linked[i], last: i == len(linked)-1})
		}
	}
	kindOf := func(r *model.Row) columns.Kind {
		if r.Clone == "" {
			return columns.KindRemote
		}
		return columns.KindClone
	}
	byGitHub := func(a, b *model.Row) int {
		return cmp.Or(cmp.Compare(githubAttention(b), githubAttention(a)), cmp.Compare(pushedAt(b), pushedAt(a)), byName(a, b))
	}
	byAttention := func(a, b *model.Row) int {
		return cmp.Or(cmp.Compare(attention(b), attention(a)), byName(a, b))
	}
	// groups writes the sections the user named, in the order written,
	// leaving out those with no rows.
	groups := func() {
		for i, rs := range grouped {
			if len(rs) == 0 {
				continue
			}
			head := groupHead(m.groups[i])
			out = append(out, line{kind: columns.KindHead, head: head, title: m.groups[i].Name, n: len(rs), note: m.headNote(rs)})
			if m.folded(head) && !filtering {
				continue
			}
			if m.lens == columns.GitHub {
				m.sortRows(rs, byGitHub)
			} else {
				// The clones by what they need, then the repositories not
				// cloned by name, as in not cloned.
				var cl, re []*model.Row
				for _, r := range rs {
					if r.Clone != "" {
						cl = append(cl, r)
					} else {
						re = append(re, r)
					}
				}
				m.sortRows(cl, byAttention)
				m.sortRows(re, byName)
				rs = append(cl, re...)
			}
			for _, r := range rs {
				push(r, kindOf(r))
			}
		}
	}
	if m.lens == columns.GitHub {
		all := append(slices.Clone(clones), remotes...)
		m.sortRows(all, byGitHub)
		for _, r := range all {
			push(r, kindOf(r))
		}
		groups()
		return out
	}
	m.sortRows(clones, byAttention)
	var quiet []*model.Row
	for _, r := range clones {
		if attention(r) == 0 && !m.top[r.Key()] {
			quiet = append(quiet, r)
			continue
		}
		push(r, columns.KindClone)
	}
	groups()
	if len(quiet) > 0 {
		out = append(out, line{kind: columns.KindHead, head: headQuiet, title: "nothing to do", n: len(quiet)})
		if !m.folded(headQuiet) || filtering {
			for _, r := range quiet {
				push(r, columns.KindClone)
			}
		}
	}
	slices.SortFunc(remotes, byName)
	switch {
	case len(remotes) > 0:
		out = append(out, line{kind: columns.KindHead, head: headNotCloned, title: "not cloned", n: len(remotes), note: m.keys.First("clone") + " clones one"})
		if !m.folded(headNotCloned) || filtering {
			for _, r := range remotes {
				push(r, columns.KindRemote)
			}
		}
	case !o.All && o.List == model.ListPartial && !filtering:
		out = append(out, line{kind: columns.KindHead, head: headUnlisted, title: "not cloned: not listed", n: -1,
			note: o.ListWhy + " · " + m.keys.First("list") + " lists every repository"})
	}
	return out
}

// sortRows sorts rs by rank, keeping the rows order holds in the order it
// gives them, ahead of the rest.
func (m *Model) sortRows(rs []*model.Row, rank func(a, b *model.Row) int) {
	slices.SortStableFunc(rs, func(a, b *model.Row) int {
		oa, ob := m.order[a.Key()], m.order[b.Key()]
		switch {
		case oa > 0 && ob > 0:
			return cmp.Compare(oa, ob)
		case oa > 0:
			return -1
		case ob > 0:
			return 1
		}
		return rank(a, b)
	})
}

// folded reports whether the section head is folded: gh tidy's own start
// folded, and those the user named as the settings say.
func (m *Model) folded(head string) bool {
	f, ok := m.folds[head]
	return !ok || f
}

// unpin lets the rows go from their places: the order they keep, and
// which were above the first head, together.
func (m *Model) unpin() { m.order, m.top = map[string]int{}, map[string]bool{} }

// relayout builds the lines again from the copy of the owner, keeping the
// cursor on the row it was on. While rows show what was done to them, or
// GitHub is being read, rows keep their places.
func (m *Model) relayout() {
	hold := m.snap.Reading != ""
	for _, r := range m.snap.Rows {
		if r.Op.Doing != "" || r.Op.Result != "" {
			hold = true
		}
	}
	if !hold {
		m.unpin()
	}
	m.lines = m.buildLines()
	// Every row shown keeps its place in its part of the list while rows
	// keep their places; top says which were above the first head, so
	// that one there whose work is done does not sink among the quiet.
	if hold && len(m.order) == 0 {
		above := true
		for i, l := range m.lines {
			if l.kind == columns.KindHead {
				above = false
				continue
			}
			if l.kind != columns.KindWorktree {
				m.order[l.key()] = i + 1
				m.top[l.key()] = above
			}
		}
	}
	found := false
	for i, l := range m.lines {
		if l.key() == m.cursor {
			m.sel, found = i, true
			break
		}
	}
	// Until the user moves, the cursor waits on the first row for the row
	// it was asked to start on, which may not be read yet. After, a row
	// gone or folded away leaves the cursor where it was on the screen.
	if !found && !m.moved {
		m.sel = 0
	}
	if len(m.lines) > 0 {
		m.sel = min(m.sel, len(m.lines)-1)
		if found || m.moved {
			m.cursor = m.lines[m.sel].key()
		}
	}
}
