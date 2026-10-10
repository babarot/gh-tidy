package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/gitx"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
)

// View draws the screen.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m *Model) hasDetail() bool { return m.screen == screenList && m.tab == tabCloned }

func (m *Model) filterLine() bool { return m.filtering || m.filter != "" }

// listHeight is how many rows the list shows.
func (m *Model) listHeight() int {
	fixed := 1 + 3 + 1 + 2 // header, rules and column headers, footer, status and help
	if m.filterLine() {
		fixed++
	}
	if m.hasDetail() {
		fixed += m.cfg.TUI.DetailHeight
	}
	return max(1, m.height-fixed)
}

func (m *Model) render() string {
	if m.width == 0 {
		return ""
	}
	if m.help {
		return m.renderHelp()
	}
	var lines []string
	lines = append(lines, m.header())
	if m.filterLine() {
		cursor := ""
		if m.filtering {
			cursor = "▏"
		}
		lines = append(lines, m.st.prompt.Render(ansi.Truncate("/ "+m.filter+cursor, m.width, "…")))
	}
	switch {
	case m.screen == screenPicker:
		lines = append(lines, m.pickerLines()...)
	case m.tab == tabNotCloned:
		lines = append(lines, m.notClonedLines()...)
	default:
		lines = append(lines, m.listLines()...)
		lines = append(lines, m.detail()...)
	}
	lines = append(lines, m.status.render(m.width, nil), m.helpLine())
	if m.confirm != nil {
		// The dialog covers the list from its first row.
		w := min(110, m.width-4)
		pad := strings.Repeat(" ", (m.width-w)/2)
		for i, l := range m.confirmLines(w) {
			if at := 4 + i; at < len(lines)-2 {
				lines[at] = pad + l
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) rule() string { return m.st.rule.Render(strings.Repeat("╌", m.width)) }

func (m *Model) header() string {
	bg := &m.st.surface
	on := func(s lipgloss.Style) lipgloss.Style { return s.Background(bg.GetBackground()) }
	left := segs{{" fleet", on(m.st.app)}}
	var right segs
	if m.screen == screenPicker {
		left = append(left, seg{" // " + locate.Abbrev(m.root) + "/" + m.host, on(m.st.tag)})
		right = segs{{fmt.Sprintf("%d owners ", len(m.owners)), on(m.st.subtle)}}
	} else {
		left = append(left, seg{" // " + m.owner + "   ", on(m.st.tag)})
		tabStyle := func(t tab) lipgloss.Style {
			if m.tab == t {
				return on(m.st.tabOn)
			}
			return on(m.st.subtle)
		}
		nc := "not cloned"
		if m.listDone && m.listErr == nil {
			nc = fmt.Sprintf("not cloned %d", m.notClonedCount())
		} else if !m.listDone {
			nc = "not cloned …"
		}
		left = append(left, seg{fmt.Sprintf("cloned %d", len(m.repos)), tabStyle(tabCloned)}, seg{"  ", on(m.st.tag)}, seg{nc, tabStyle(tabNotCloned)})

		attention, stale := 0, 0
		for _, r := range m.repos {
			if r.Score() > 0 {
				attention++
			}
			if m.stale(r) {
				stale++
			}
		}
		if m.scanning {
			right = append(right, seg{fmt.Sprintf("reading %d/%d · ", len(m.repos), m.scanTotal), on(m.st.accent)})
		} else if !m.remoteDone {
			right = append(right, seg{"asking GitHub · ", on(m.st.accent)})
		}
		right = append(right, seg{fmt.Sprintf("%d need attention", attention), on(m.st.subtle)})
		if stale > 0 {
			right = append(right, seg{" · ", on(m.st.subtle)}, seg{fmt.Sprintf("%d stale", stale), on(m.st.prompt)})
		}
		right = append(right, seg{" · sort: " + strings.ReplaceAll(config.Sorts[m.sortIdx], "_", " "), on(m.st.subtle)})
		if m.showAll {
			right = append(right, seg{" · all", on(m.st.subtle)})
		}
		right = append(right, seg{" ", on(m.st.subtle)})
	}
	gap := m.width - left.width() - right.width()
	if gap < 1 {
		return left.render(m.width, bg)
	}
	return left.render(left.width(), bg) + bg.Render(strings.Repeat(" ", gap)) + right.render(right.width(), bg)
}

// notClonedCount is how many are not cloned, the filter aside.
func (m *Model) notClonedCount() int {
	f := m.filter
	m.filter = ""
	defer func() { m.filter = f }()
	return len(m.notCloned())
}

// lay draws one row: the selection bar, then each column's cells.
func (m *Model) lay(cols []column, sel bool, cell func(column) segs) string {
	var bg *lipgloss.Style
	bar := segs{{"  ", lipgloss.NewStyle()}}
	if sel {
		bg = &m.st.surface
		bar = segs{{"▎ ", m.st.bar}}
	}
	var b strings.Builder
	b.WriteString(bar.render(2, bg))
	used := 2
	for i, c := range cols {
		w := c.width
		if w == 0 {
			w = max(0, m.width-used)
		}
		if i < len(cols)-1 || c.width != 0 {
			b.WriteString(cell(c).render(w, bg))
			used += w
			gap := min(2, max(0, m.width-used))
			b.WriteString(segs{}.render(gap, bg))
			used += gap
		} else {
			b.WriteString(cell(c).render(w, bg))
			used += w
		}
	}
	if used < m.width {
		b.WriteString(segs{}.render(m.width-used, bg))
	}
	return b.String()
}

func (m *Model) listLines() []string {
	cols := m.columns(m.width)
	lines := []string{m.rule(), m.lay(cols, false, func(c column) segs { return segs{{c.title, m.st.colHdr}} }), m.rule()}
	rows := m.rows()
	h := m.listHeight()
	end := min(len(rows), m.offset+h)
	for i := m.offset; i < end; i++ {
		x := rows[i]
		sel := i == m.sel
		if x.wt != nil {
			lines = append(lines, m.worktreeLine(x, sel))
			continue
		}
		lines = append(lines, m.lay(cols, sel, func(c column) segs { return m.cell(c.key, x.r) }))
	}
	for i := end - m.offset; i < h; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, m.footer(len(rows), end))
	return lines
}

func (m *Model) footer(rows, end int) string {
	var s segs
	shown := len(m.visible())
	if m.scanning && len(m.repos) == 0 {
		s = s.add("reading clones…", m.st.muted)
	} else {
		s = s.add(fmt.Sprintf("%d repositories", shown), m.st.muted)
	}
	if hidden := len(m.repos) - shown; hidden > 0 && !m.showAll && m.filter == "" {
		s = append(s, seg{fmt.Sprintf(" · %d with nothing to say hidden (%s)", hidden, m.keys.first(opShowAll)), m.st.dim})
	}
	if more := rows - end; more > 0 {
		s = append(s, seg{fmt.Sprintf(" · ↓ %d more", more), m.st.muted})
	}
	return " " + s.render(m.width-1, nil)
}

func (m *Model) worktreeLine(x row, sel bool) string {
	w := x.wt
	branch := w.Branch
	if branch == "" {
		branch = "(detached)"
	}
	tree := "├─"
	if x.last {
		tree = "└─"
	}
	s := segs{{"  " + tree + " ", m.st.rule}, {"⌥ " + worktreeName(*w), m.st.worktree}}
	s = s.add(branch, m.st.strong)
	s = s.join(m.worktreeState(x.r, *w, false))
	if w.Locked {
		s = s.add("locked", m.st.muted)
	}
	s = s.add(locate.Abbrev(w.Path), m.st.dim)
	var bg *lipgloss.Style
	bar := segs{{"  ", lipgloss.NewStyle()}}
	if sel {
		bg = &m.st.surface
		bar = segs{{"▎ ", m.st.bar}}
	}
	return bar.render(2, bg) + s.render(m.width-2, bg)
}

// detail is the pane below the list for the selected clone.
func (m *Model) detail() []string {
	h := m.cfg.TUI.DetailHeight
	rows := m.rows()
	inner := h - 2
	var left, right []segs
	if m.sel < len(rows) {
		r := rows[m.sel].r
		left, right = m.detailLeft(r), m.detailRight(r, rows[m.sel].wt, inner)
	}
	colW := max(10, (m.width-4-3)/2)
	var body []string
	for i := range inner {
		var l, rt segs
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			rt = right[i]
		}
		body = append(body, l.render(colW, nil)+"   "+rt.render(m.width-4-3-colW, nil))
	}
	title := ""
	if m.sel < len(rows) {
		title = rows[m.sel].r.Name
	}
	return frame(m.st, title, body, m.width)
}

func frame(st styles, title string, body []string, width int) []string {
	return frameColored(st, title, body, width, lipgloss.NewStyle().Foreground(st.border))
}

// frameColored draws body in a rounded frame of b's color, title on its top
// edge.
func frameColored(st styles, title string, body []string, width int, b lipgloss.Style) []string {
	top := "╭─"
	if title != "" {
		top += " " + st.strong.Bold(true).Render(title) + b.Render(" ")
	}
	top = b.Render("╭─") + strings.TrimPrefix(top, "╭─")
	if fill := width - 1 - ansi.StringWidth(top); fill > 0 {
		top += b.Render(strings.Repeat("─", fill))
	}
	top += b.Render("╮")
	out := []string{top}
	for _, l := range body {
		out = append(out, b.Render("│")+" "+l+" "+b.Render("│"))
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", max(0, width-2))+"╯"))
	return out
}

func (m *Model) detailLeft(r *repo.Repo) []segs {
	st := m.st
	label := func(l string) segs { return segs{{fmt.Sprintf("%-9s", l), st.muted}} }
	var lines []segs

	where := segs{{r.Name, st.name}}
	switch {
	case !r.HasOrigin():
		where = where.add("no origin, never pushed", st.warn)
	case r.Slug == "":
		where = where.add(r.OriginURL, st.muted)
	default:
		where = where.add(r.Slug, st.muted)
		if g, ok := m.remoteFor(r); ok {
			v := strings.ToLower(g.Visibility)
			if g.Archived {
				v += " · archived"
			}
			where = where.add(v, st.dim)
		} else if m.remoteDone && m.remoteErr == nil && strings.EqualFold(strings.SplitN(r.Slug, "/", 2)[0], m.owner) {
			where = where.add("not found on GitHub", st.warn)
		}
	}
	lines = append(lines, where)

	br := append(label("branch"), m.branchSegs(r)...)
	if r.Upstream != "" {
		br = br.add("→ "+r.Upstream, st.dim)
	} else if r.Head != "" {
		br = br.add("no upstream", st.dim)
	}
	if s := m.syncSegs(r); len(s) > 0 {
		br = append(br, seg{"  ", st.dim})
		br = append(br, s...)
	}
	if s := m.baseSegs(r); len(s) > 0 {
		br = append(br, seg{" ", st.dim})
		br = append(br, s...)
	}
	lines = append(lines, br)

	status := label("status")
	var parts segs
	if r.Unstaged > 0 {
		parts = parts.add(fmt.Sprintf("%d unstaged", r.Unstaged), st.prompt)
	}
	if r.Conflicts > 0 {
		parts = parts.add(fmt.Sprintf("%d conflicts", r.Conflicts), st.warn)
	}
	if r.Staged > 0 {
		parts = parts.add(fmt.Sprintf("%d staged", r.Staged), st.ok)
	}
	if r.Untracked > 0 {
		parts = parts.add(fmt.Sprintf("%d untracked", r.Untracked), st.muted)
	}
	if r.Stash > 0 {
		parts = parts.add(fmt.Sprintf("%d stashed", r.Stash), st.stash)
	}
	if len(parts) == 0 {
		parts = segs{{"clean", st.dim}}
	}
	lines = append(lines, append(status, parts...))

	last := label("last")
	if !r.LastCommit.IsZero() {
		last = append(last, seg{ago(r.LastCommit, m.now) + "  ", st.muted}, seg{r.LastSubject, st.subtle})
	}
	lines = append(lines, last)

	fetched := append(label("fetched"), m.fetchedSegs(r)...)
	if m.stale(r) {
		fetched = fetched.add("GitHub has newer commits on "+r.Default, st.prompt)
	}
	lines = append(lines, fetched)

	def := label("default")
	switch {
	case r.Default == "":
		def = append(def, seg{"unknown (git remote set-head origin -a)", st.warn})
	default:
		def = append(def, seg{r.Default, st.subtle})
		def = def.add("from "+r.DefaultSource, st.dim)
		if r.OriginHEAD != "" && r.OriginHEAD != r.Default {
			def = def.add("origin/HEAD says "+r.OriginHEAD, st.prompt)
		}
	}
	lines = append(lines, def)
	if r.Err != nil {
		lines = append([]segs{{{r.Err.Error(), st.warn}}}, lines...)
	}
	return lines
}

// worktreeState says what a worktree has: changes, commits the default
// branch does not have yet (or merged, when it has them all), and how far
// the default branch has moved on. short writes it for the detail pane.
func (m *Model) worktreeState(r *repo.Repo, w repo.Worktree, short bool) segs {
	st := m.st
	var s segs
	dirty := w.Dirty
	if w.Main {
		dirty = r.Dirty() + r.Untracked
	}
	switch {
	case w.Prunable:
		return s.add("directory gone", st.warn)
	case dirty > 0 && short:
		s = s.add(fmt.Sprintf("%d*", dirty), st.prompt)
	case dirty > 0:
		s = s.add(fmt.Sprintf("%d changed", dirty), st.prompt)
	case !short:
		s = s.add("clean", st.dim)
	}
	if w.Measured {
		switch {
		case w.Ahead > 0 && short:
			s = s.add(fmt.Sprintf(">%d", w.Ahead), st.worktree)
		case w.Ahead > 0:
			s = s.add(fmt.Sprintf("%d not in %s", w.Ahead, r.Default), st.worktree)
		default:
			s = s.add("merged", st.ok)
		}
		if w.Behind > 0 {
			s = s.add(fmt.Sprintf("⇣%d", w.Behind), st.accent)
		}
	}
	return s
}

// detailRight is the right column of the detail pane, n lines at most: the
// worktrees, then the other branches with something to say.
func (m *Model) detailRight(r *repo.Repo, focus *repo.Worktree, n int) []segs {
	st := m.st
	var lines []segs
	lines = append(lines, segs{{"WORKTREES", st.subtle.Bold(true)}, {fmt.Sprintf(" %d", len(r.Worktrees)), st.dim}})
	shown := 0
	for _, w := range r.Worktrees {
		if shown == 2 {
			break
		}
		if w.Main && len(r.Worktrees) > 1 {
			continue
		}
		mark := "  "
		if focus != nil && focus.Path == w.Path {
			mark = "▸ "
		}
		s := segs{{mark, st.accent}, {"⌥ " + worktreeName(w), st.worktree}}
		s = s.add(w.Branch, st.strong)
		lines = append(lines, s.join(m.worktreeState(r, w, true)))
		shown++
	}
	if rest := len(r.Linked()) - shown; rest > 0 {
		lines = append(lines, segs{{fmt.Sprintf("  +%d more (%s)", rest, m.keys.first(opExpand)), st.dim}})
	}

	up, local, gone := r.Unpushed(), r.NoUpstream(), r.GoneUpstream()
	var sum segs
	if len(up) > 0 {
		sum = sum.add(fmt.Sprintf("↑%d unpushed", len(up)), st.worktree)
	}
	if len(gone) > 0 {
		sum = sum.add(fmt.Sprintf("✗%d gone", len(gone)), st.dim)
	}
	if len(local) > 0 {
		sum = sum.add(fmt.Sprintf("·%d local", len(local)), st.muted)
	}
	if len(sum) == 0 {
		sum = segs{{"nothing else to push", st.dim}}
	}
	lines = append(lines, append(segs{{"BRANCHES ", st.subtle.Bold(true)}}, sum...))

	// Unpushed first: work that exists nowhere else. Then gone, then local,
	// each newest first.
	type entry struct {
		b   gitx.Branch
		tag seg
	}
	var entries []entry
	newest := func(bs []gitx.Branch) []gitx.Branch {
		bs = slices.Clone(bs)
		slices.SortFunc(bs, func(a, b gitx.Branch) int { return int(b.Date - a.Date) })
		return bs
	}
	for _, b := range newest(up) {
		entries = append(entries, entry{b, seg{fmt.Sprintf("ahead %d", b.Ahead), st.worktree}})
	}
	for _, b := range newest(gone) {
		entries = append(entries, entry{b, seg{"gone", st.dim}})
	}
	for _, b := range newest(local) {
		entries = append(entries, entry{b, seg{"local", st.muted}})
	}
	room := n - len(lines)
	for i, e := range entries {
		if i == room-1 && len(entries) > room {
			lines = append(lines, segs{{fmt.Sprintf("  +%d more", len(entries)-i), st.dim}})
			break
		}
		if i >= room {
			break
		}
		lines = append(lines, segs{{"  " + e.b.Name, st.strong}, {" ", st.dim}, e.tag, {" " + ago(time.Unix(e.b.Date, 0), m.now), st.dim}})
	}
	return lines
}

func (m *Model) notClonedLines() []string {
	cols := []column{{"name", "REPO", 34}, {"vis", "VISIBILITY", 10}, {"stars", "STARS", 6}, {"pushed", "PUSHED", 10}, {"fork", "", 0}}
	lines := []string{m.rule(), m.lay(cols, false, func(c column) segs { return segs{{c.title, m.st.colHdr}} }), m.rule()}
	nc := m.notCloned()
	h := m.listHeight()
	end := min(len(nc), m.offset+h)
	for i := m.offset; i < end; i++ {
		g := nc[i]
		lines = append(lines, m.lay(cols, i == m.sel, func(c column) segs {
			switch c.key {
			case "name":
				return segs{{g.Name, m.st.name}}
			case "vis":
				return segs{{strings.ToLower(g.Visibility), m.st.muted}}
			case "stars":
				return segs{{fmt.Sprintf("★%d", g.Stars), m.st.muted}}
			case "pushed":
				return segs{{ago(g.PushedAt, m.now), m.st.muted}}
			case "fork":
				if g.Fork {
					return segs{{"fork", m.st.dim}}
				}
			}
			return nil
		}))
	}
	for i := end - m.offset; i < h; i++ {
		lines = append(lines, "")
	}
	var s segs
	switch {
	case !m.listDone:
		s = segs{{"asking GitHub…", m.st.muted}}
	case m.listErr != nil:
		s = segs{{"GitHub: " + m.listErr.Error(), m.st.warn}}
	default:
		s = segs{{fmt.Sprintf("%d not cloned, archived left out · they would go to %s", len(nc), locate.Abbrev(m.root+"/"+m.host+"/"+m.owner)+"/<name>"), m.st.muted}}
	}
	return append(lines, " "+s.render(m.width-1, nil))
}

func (m *Model) pickerLines() []string {
	cols := []column{{"owner", "OWNER", 30}, {"clones", "CLONES", 8}, {"x", "", 0}}
	lines := []string{m.rule(), m.lay(cols, false, func(c column) segs { return segs{{c.title, m.st.colHdr}} }), m.rule()}
	owners := m.pickerOwners()
	h := m.listHeight()
	end := min(len(owners), m.offset+h)
	for i := m.offset; i < end; i++ {
		o := owners[i]
		lines = append(lines, m.lay(cols, i == m.sel, func(c column) segs {
			switch c.key {
			case "owner":
				return segs{{o.Name, m.st.name}}
			case "clones":
				return segs{{fmt.Sprintf("%6d", o.Clones), m.st.strong}}
			}
			return nil
		}))
	}
	for i := end - m.offset; i < h; i++ {
		lines = append(lines, "")
	}
	more := ""
	if n := len(owners) - end; n > 0 {
		more = fmt.Sprintf(" · ↓ %d more", n)
	}
	return append(lines, " "+segs{{fmt.Sprintf("%d owners%s", len(owners), more), m.st.muted}}.render(m.width-1, nil))
}

func (m *Model) helpLine() string {
	k := m.keys.first
	both := func(a, b string) string {
		switch {
		case a == "":
			return b
		case b == "":
			return a
		}
		return a + "/" + b
	}
	var ops [][2]string
	switch {
	case m.confirm != nil:
		ops = [][2]string{{"enter", "remove"}, {"esc", "cancel"}}
	case m.filtering:
		ops = [][2]string{{"enter", "keep filter"}, {"esc", "clear"}}
	case m.screen == screenPicker:
		ops = [][2]string{{k(opExpand), "open"}, {k(opSearch), "filter"}, {k(opOpenBrowser), "browser"}, {k(opQuit), "quit"}}
	case m.tab == tabNotCloned:
		ops = [][2]string{{k(opClone), "clone"}, {k(opOpenBrowser), "browser"}, {k(opCopyURL), "copy url"},
			{k(opSwitchTab), "cloned"}, {k(opSearch), "filter"}, {k(opHelp), "keys"}, {k(opQuit), "quit"}}
	default:
		ops = [][2]string{{k(opExpand), "worktrees"}, {both(k(opFetch), k(opFetchAll)), "fetch"}, {both(k(opPull), k(opPullAll)), "pull"},
			{k(opCleanup), "clean up"}, {k(opOpenBrowser), "browser"}, {k(opReveal), "finder"}, {both(k(opCopyPath), k(opCopyURL)), "copy"},
			{k(opShowAll), "all"}, {k(opSwitchTab), "not cloned"}, {k(opSearch), "filter"}, {k(opHelp), "keys"}, {k(opQuit), "quit"}}
	}
	var s segs
	for _, o := range ops {
		if o[0] == "" {
			continue // the operation has no key
		}
		if len(s) > 0 {
			s = append(s, seg{" · ", m.st.rule})
		}
		s = append(s, seg{o[0], m.st.key}, seg{" " + o[1], m.st.muted})
	}
	return " " + s.render(m.width-1, nil)
}

func (m *Model) renderHelp() string {
	lines := []string{m.header(), m.rule()}
	for _, d := range defaultKeys {
		keys := strings.Join(m.keys.byOp[d.op], ", ")
		if keys == "" {
			keys = "(none)"
		}
		lines = append(lines, " "+segs{{fmt.Sprintf("%-18s", keys), m.st.key}, {d.help, m.st.subtle}}.render(m.width-1, nil))
	}
	for _, a := range m.cfg.Actions {
		lines = append(lines, " "+segs{{fmt.Sprintf("%-18s", a.Key), m.st.key}, {a.Name, m.st.subtle}}.render(m.width-1, nil))
	}
	lines = append(lines, "", " "+segs{{"STATUS", m.st.colHdr}, {"  * unstaged  + staged  % untracked  $ stashed", m.st.muted}}.render(m.width-1, nil),
		" "+segs{{"SYNC  ", m.st.colHdr}, {"  <N behind  >N ahead  <N>M diverged  = up to date  ? GitHub has newer commits", m.st.muted}}.render(m.width-1, nil),
		" "+segs{{"BASE  ", m.st.colHdr}, {"  ⇣N the default branch has N commits HEAD does not", m.st.muted}}.render(m.width-1, nil),
		" "+segs{{"BRANCHES", m.st.colHdr}, {"  ↑N unpushed  ·N no upstream  ✗N upstream gone", m.st.muted}}.render(m.width-1, nil),
		"", " "+m.st.dim.Render("any key closes this"))
	return strings.Join(lines, "\n")
}
