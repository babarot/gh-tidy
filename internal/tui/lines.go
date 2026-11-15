package tui

import (
	"cmp"
	"slices"
	"strings"

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
// alerts, then checks failing on the default branch, then open pull
// requests and issues.
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

// buildLines lays out the owner's rows for the lens. order, when it holds
// a row, keeps it where it was, so that values arriving do not move rows
// under the cursor; rows it does not hold are placed by rank after them.
func (m *Model) buildLines() []line {
	o := &m.snap
	var clones, remotes []*model.Row
	for i := range o.Rows {
		r := &o.Rows[i]
		name := line{kind: columns.KindClone, row: r}.name()
		if !matches(r, name, m.filter) {
			continue
		}
		switch {
		case r.Clone != "":
			clones = append(clones, r)
		case notCloned(r) && !o.All:
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
	if m.lens == columns.GitHub {
		all := append(slices.Clone(clones), remotes...)
		m.sortRows(all, func(a, b *model.Row) int {
			return cmp.Or(cmp.Compare(githubAttention(b), githubAttention(a)), cmp.Compare(pushedAt(b), pushedAt(a)), byName(a, b))
		})
		for _, r := range all {
			kind := columns.KindClone
			if r.Clone == "" {
				kind = columns.KindRemote
			}
			push(r, kind)
		}
		return out
	}
	m.sortRows(clones, func(a, b *model.Row) int {
		return cmp.Or(cmp.Compare(attention(b), attention(a)), byName(a, b))
	})
	var quiet []*model.Row
	for _, r := range clones {
		if attention(r) == 0 && m.order[r.Key()] == 0 {
			quiet = append(quiet, r)
			continue
		}
		push(r, columns.KindClone)
	}
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

// folded reports whether the section head is folded; both sections start
// folded.
func (m *Model) folded(head string) bool {
	f, ok := m.folds[head]
	return !ok || f
}

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
		m.order = map[string]int{}
	}
	m.lines = m.buildLines()
	// The rows above the first head keep their places; those in a section
	// stay in it.
	if hold && len(m.order) == 0 {
		for i, l := range m.lines {
			if l.kind == columns.KindHead {
				break
			}
			if l.kind != columns.KindWorktree {
				m.order[l.key()] = i + 1
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
