package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/tui/columns"
	"github.com/babarot/gh-tidy/internal/tui/keymap"
	"github.com/babarot/gh-tidy/internal/tui/statusline"
)

// The rows above the list (header, rules and column titles) and the
// width of the facts on the preview's right.
const (
	listTop    = 4
	factsWidth = 50
	factsFrom  = 110 // narrower than this, the pane shows no facts
	allRepo    = 6   // what the repository column widens by in all, for the owner
)

// listHeight is how many lines the list shows.
func (m *Model) listHeight() int {
	return max(1, m.height-m.ph-9)
}

// scroll keeps the cursor on the screen.
func (m *Model) scroll() {
	h := m.listHeight()
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+h {
		m.offset = m.sel - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, len(m.lines)-h)))
}

// View draws the screen.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	ls := m.screen()
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = m.st.paint(l, false)
	}
	if m.popup != nil {
		out = m.overlay(ls)
	}
	return strings.Join(out, "\n")
}

// screen lays out every line of the screen, and where the mouse can
// click.
func (m *Model) screen() []drawn {
	m.hits = m.hits[:0]
	w := m.width
	var ls []drawn
	ls = append(ls, m.header())
	rule := drawn{parts: []cell{{text: strings.Repeat("╌", w), role: roleBorder}}}
	cols, ws := m.layout()
	ls = append(ls, rule, m.titles(cols, ws), rule)
	h := m.listHeight()
	for i := range h {
		j := m.offset + i
		if j >= len(m.lines) {
			ls = append(ls, drawn{parts: cut(nil, w)})
			continue
		}
		ls = append(ls, m.row(m.lines[j], j == m.sel, cols, ws))
	}
	ls = append(ls, m.summary())
	ls = append(ls, m.preview(len(ls))...)
	ls = append(ls, m.statusLine(), m.hintLine(len(ls)+1))
	return ls
}

// layout picks the lens's columns that fit the width.
func (m *Model) layout() ([]columns.Column, []int) {
	cs := slices.Clone(m.cols[m.lens])
	if m.snap.All {
		for i := range cs {
			if cs[i].Name == "repo" {
				cs[i].Width += allRepo
			}
		}
	}
	return columns.Fit(cs, m.width-2)
}

func (m *Model) ctx() columns.Ctx {
	return columns.Ctx{Owner: &m.snap, Symbols: m.sym, Now: m.now()}
}

func (m *Model) header() drawn {
	w := m.width
	ow := 0
	for _, o := range m.owners {
		ow = max(ow, ansi.StringWidth(o))
	}
	left := []cell{{text: " tidy", role: roleAccent, bold: true}, {text: " // ", role: roleDim}}
	x := width(left)
	add := func(cs []cell, act func(*Model) tea.Cmd) {
		if act != nil {
			m.hits = append(m.hits, hit{y: 0, x0: x, x1: x + width(cs), act: act})
		}
		left = append(left, cs...)
		x += width(cs)
	}
	add([]cell{{text: "‹ ", role: roleMuted}}, func(m *Model) tea.Cmd { m.stepOwner(-1); return nil })
	add([]cell{{text: m.owner, role: roleStrong, bold: true}}, func(m *Model) tea.Cmd { m.open(m.ownerPopup()); return nil })
	add([]cell{{text: " ›", role: roleMuted}}, func(m *Model) tea.Cmd { m.stepOwner(1); return nil })
	add([]cell{{text: strings.Repeat(" ", ow-ansi.StringWidth(m.owner)+4)}}, nil)
	for i, l := range columns.Lenses {
		if i > 0 {
			add([]cell{{text: " │ ", role: roleBorder}}, nil)
		}
		c := cell{text: string(l), role: roleSubtle}
		if l == m.lens {
			c = cell{text: string(l), role: roleAccent, bold: true, underline: true}
		}
		add([]cell{c}, func(m *Model) tea.Cmd { m.setLens(l); return nil })
	}
	right := m.headerRight()
	gap := w - width(left) - width(right)
	if gap < 1 {
		return drawn{parts: cut(left, w), bg: true}
	}
	parts := append(left, cell{text: strings.Repeat(" ", gap)})
	return drawn{parts: append(parts, right...), bg: true}
}

// headerRight says what is being read, or what the owner's clones need.
func (m *Model) headerRight() []cell {
	o := &m.snap
	dot := cell{text: " · ", role: roleBorder}
	var r []cell
	if o.Reading != "" {
		r = append(r, cell{text: o.Reading, role: roleAccent})
		if o.FromCache && !o.ListAt.IsZero() {
			r = append(r, cell{text: " · as of " + columns.Ago(o.ListAt, m.now()), role: rolePrompt})
		}
	} else {
		up, changed, done := 0, 0, 0
		x := m.ctx()
		for i := range o.Rows {
			row := &o.Rows[i]
			if !row.Local.Has {
				continue
			}
			c := row.Local.Value
			if columns.Main(row).CanUpdate() {
				up++
			}
			if c.Dirty()+c.Untracked > 0 || slices.ContainsFunc(c.Linked(), func(w model.Worktree) bool { return w.Dirty > 0 }) {
				changed++
			}
			for _, w := range c.Linked() {
				if v, _, ok := columns.Verdict(row, w, x.Now); ok && columns.Removes(v) {
					done++
				}
			}
		}
		r = append(r, cell{text: fmt.Sprintf("%d can update", up), role: roleAccent}, dot,
			cell{text: fmt.Sprintf("%d with changes", changed), role: rolePrompt}, dot,
			cell{text: fmt.Sprintf("%d worktree%s done", done, plural(done)), role: roleOK})
	}
	switch {
	case o.All:
		r = append(r, cell{text: " · GitHub as last read", role: roleMuted})
	case o.List == model.ListUnreadable:
		r = append(r, cell{text: " · GitHub: can't read the list", role: roleWarn})
	}
	return append(r, cell{text: " "})
}

func (m *Model) titles(cs []columns.Column, ws []int) drawn {
	parts := []cell{{text: "  "}}
	for i, c := range cs {
		if i > 0 {
			parts = append(parts, cell{text: strings.Repeat(" ", columns.Gap)})
		}
		parts = append(parts, cut([]cell{{text: c.Title, role: roleStrong, bold: true}}, ws[i])...)
	}
	return drawn{parts: cut(parts, m.width)}
}

func (m *Model) row(l line, sel bool, cs []columns.Column, ws []int) drawn {
	bar := cell{text: "  "}
	if sel {
		bar = cell{text: "▎ ", role: roleAccent}
	}
	if l.kind == columns.KindHead {
		mark := "▸ "
		if l.head == headUnlisted {
			mark = "  "
		} else if !m.folded(l.head) || m.filter != "" {
			mark = "▾ "
		}
		parts := []cell{bar, {text: mark, role: roleMuted}, {text: l.title, role: roleSubtle, bold: true}}
		if l.n >= 0 {
			parts = append(parts, cell{text: "  " + strconv.Itoa(l.n), role: roleDim})
		}
		if l.note != "" {
			parts = append(parts, cell{text: "   " + l.note, role: roleDim})
		}
		return drawn{parts: cut(parts, m.width), bg: sel}
	}
	x := m.ctx()
	parts := []cell{bar}
	for i, c := range cs {
		if i > 0 {
			parts = append(parts, cell{text: strings.Repeat(" ", columns.Gap)})
		}
		content := fromCell(c.Cell(x, l.item()))
		if c.Name == "repo" && l.kind == columns.KindWorktree {
			tree := "  ├ "
			if l.last {
				tree = "  └ "
			}
			content = append([]cell{{text: tree, role: roleBorder}}, content...)
		}
		parts = append(parts, cut(content, ws[i])...)
	}
	return drawn{parts: cut(parts, m.width), bg: sel}
}

// summary counts the rows, or shows the filter being typed.
func (m *Model) summary() drawn {
	if m.filtering || m.filter != "" {
		parts := []cell{{text: " / ", role: roleAccent}, {text: m.filter, role: rolePrompt}}
		if m.filtering {
			parts = append(parts, cell{text: "▏", role: roleAccent})
		}
		return drawn{parts: cut(parts, m.width)}
	}
	clones, remotes := 0, 0
	for _, r := range m.snap.Rows {
		switch {
		case r.Clone != "":
			clones++
		case notCloned(&r) && !m.snap.All:
			remotes++
		}
	}
	s := fmt.Sprintf(" %d clone%s", clones, plural(clones))
	if remotes > 0 {
		s += fmt.Sprintf(" · %d not cloned", remotes)
	}
	return drawn{parts: cut([]cell{{text: s, role: roleMuted}}, m.width)}
}

// pane is what the preview shows of a line: its title, the sections on
// the left, and the facts on the right.
type pane struct {
	title string
	parts []columns.Part
	facts []columns.Fact
}

func (m *Model) paneOf(l line) pane {
	x := m.ctx()
	switch l.kind {
	case columns.KindHead:
		p := columns.Part{Head: columns.Cell{{Text: l.title, Role: roleSubtle, Bold: true}}}
		if l.head == headUnlisted {
			p.Lines = []columns.Cell{{{Text: "  gh tidy reads the list of an owner that is yours or your organization's; for any", Role: roleMuted}},
				{{Text: "  other owner it looks up only what you have cloned, so a big organization costs nothing", Role: roleMuted}},
				{{Text: "  until you ask.", Role: roleMuted}}}
		} else {
			p.Lines = []columns.Cell{{{Text: "  " + m.keys.First("expand") + " or a click folds and unfolds it", Role: roleDim}}}
		}
		return pane{parts: []columns.Part{p}}
	}
	it := l.item()
	p := pane{title: l.name(), facts: columns.Facts(x, it, m.home)}
	if l.kind == columns.KindWorktree {
		p.title = m.sym.Worktree + " " + l.name()
	}
	if m.lens == columns.GitHub || l.kind == columns.KindRemote {
		p.parts = columns.GitHubParts(x, it)
		return p
	}
	if !l.row.Local.Has {
		p.parts = []columns.Part{{Head: columns.Cell{{Text: "READING THE CLONE…", Role: roleDim, Bold: true}}}}
		return p
	}
	for _, s := range m.sections {
		if !slices.Contains(s.Kinds, l.kind) {
			continue
		}
		if part := s.Build(x, it, m.detail); part != nil {
			part.Name = s.Name
			p.parts = append(p.parts, *part)
		}
	}
	if len(p.parts) == 0 {
		p.parts = []columns.Part{columns.Quiet(it)}
	}
	return p
}

// preview draws the pane below the list; top is the screen line it
// starts on.
func (m *Model) preview(top int) []drawn {
	w := m.width
	l, ok := m.selected()
	var p pane
	if ok {
		p = m.paneOf(l)
	}
	if ok && l.key() != m.pkey {
		m.pkey, m.pscroll = l.key(), 0
	}
	inner := w - 4
	lw := inner
	showFacts := w >= factsFrom && len(p.facts) > 0
	if showFacts {
		lw = inner - factsWidth - 3
	}
	type left struct {
		parts []cell
		fold  string
	}
	var lefts []left
	for _, part := range p.parts {
		folded := part.Name != "" && m.pfold[part.Name]
		mark := "▾ "
		if folded {
			mark = "▸ "
		}
		if part.Name == "" {
			mark = ""
		}
		lefts = append(lefts, left{parts: append([]cell{{text: mark, role: roleMuted}}, fromCell(part.Head)...), fold: part.Name})
		if folded {
			continue
		}
		for _, c := range part.Lines {
			lefts = append(lefts, left{parts: fromCell(c)})
		}
	}
	m.pscroll = max(0, min(m.pscroll, max(0, len(lefts)-m.ph)))
	more := len(lefts) - m.pscroll - m.ph
	title := ""
	if p.title != "" {
		title = "─ " + p.title + " "
	}
	tail := " " + m.keys.First("preview_grow") + "/" + m.keys.First("preview_shrink") + " size "
	if more > 0 {
		tail = fmt.Sprintf(" ↓ %d more ·", more) + tail
	}
	fill := max(0, w-2-ansi.StringWidth(title)-ansi.StringWidth(tail))
	out := []drawn{{parts: cut([]cell{{text: "╭" + title + strings.Repeat("─", fill) + tail + "╮", role: roleBorder}}, w)}}
	for i := range m.ph {
		var lp []cell
		if j := m.pscroll + i; j < len(lefts) {
			lp = lefts[j].parts
			if f := lefts[j].fold; f != "" {
				m.hits = append(m.hits, hit{y: top + 1 + i, x0: 2, x1: 2 + lw, act: func(m *Model) tea.Cmd { m.pfold[f] = !m.pfold[f]; return nil }})
			}
		}
		parts := append([]cell{{text: "│ ", role: roleBorder}}, cut(lp, lw)...)
		if showFacts {
			var rp []cell
			if i < len(p.facts) {
				f := p.facts[i]
				rp = append(cut([]cell{{text: f.Label, role: roleSubtle}}, 9), fromCell(f.Value)...)
			}
			parts = append(parts, cell{text: " │ ", role: roleDim})
			parts = append(parts, cut(rp, factsWidth)...)
		}
		parts = append(parts, cell{text: " │", role: roleBorder})
		out = append(out, drawn{parts: cut(parts, w)})
	}
	out = append(out, drawn{parts: cut([]cell{{text: "╰" + strings.Repeat("─", max(0, w-2)) + "╯", role: roleBorder}}, w)})
	return out
}

// statusLine says what was said last, or else what is being done: the
// spinner, the first kind of work not done with its count and its bar,
// and the counts of the others.
func (m *Model) statusLine() drawn {
	parts := []cell{{text: " "}}
	if msg, ok := m.line.Current(m.now()); ok {
		parts = append(parts, cell{text: msg.Text, role: kindRole[msg.Kind]})
		return drawn{parts: cut(parts, m.width)}
	}
	if a := m.eng.Activity(); a.Busy() {
		parts = append(parts, m.progress(a)...)
	}
	return drawn{parts: cut(parts, m.width)}
}

// kindRole is the color of a message of each kind.
var kindRole = map[statusline.Kind]columns.Role{
	statusline.Info: roleMuted, statusline.Working: roleAccent, statusline.Done: roleOK,
	statusline.Refused: rolePrompt, statusline.Failed: roleWarn,
}

// countWords say what a count of each kind of work does.
var countWords = map[string]string{"clones": "reading clones", "GitHub": "reading GitHub", "fetched": "fetching", "updated": "updating",
	"cleaned": "cleaning up", "cloned": "cloning"}

// progressBar is how many cells the bar takes.
const progressBar = 10

// progress is the spinner and the counts of a run going on.
func (m *Model) progress(a model.Activity) []cell {
	var open []model.Count
	for _, c := range a.Counts {
		if c.Done < c.Total {
			open = append(open, c)
		}
	}
	parts := []cell{{text: statusline.Spinner(m.now()) + " ", role: roleAccent}}
	if len(open) == 0 {
		return append(parts, cell{text: strings.ToLower(a.Verb), role: roleAccent})
	}
	first := open[0]
	words := countWords[first.Name]
	if words == "" {
		words = first.Name
	}
	full := statusline.Bar(first.Done, first.Total, progressBar)
	parts = append(parts, cell{text: fmt.Sprintf("%s %d/%d ", words, first.Done, first.Total), role: roleAccent},
		cell{text: strings.Repeat("▰", full), role: roleAccent}, cell{text: strings.Repeat("▱", progressBar-full), role: roleBorder})
	for _, c := range open[1:] {
		parts = append(parts, cell{text: fmt.Sprintf(" · %s %d/%d", c.Name, c.Done, c.Total), role: roleMuted})
	}
	return parts
}

// hint is a key in the key line and what it does.
type hint struct {
	key, does string
}

func (m *Model) hints() []hint {
	if m.popup != nil {
		return m.popupHints(true)
	}
	if m.filtering {
		return []hint{{m.keys.First("accept"), m.keys.Help("accept")}, {"esc", "clear"}}
	}
	h := func(a keymap.Action) hint { return hint{m.keys.First(a), m.keys.Help(a)} }
	other := columns.Lenses[(slices.Index(columns.Lenses, m.lens)+1)%len(columns.Lenses)]
	lens := hint{m.keys.First("lens"), string(other)}
	pane := hint{strings.TrimSpace(m.keys.First("preview_grow") + " " + m.keys.First("preview_shrink")), "preview"}
	if m.lens == columns.GitHub {
		return []hint{lens, h("open_browser"), h("clone"), h("search"), h("choose_owner"), h("help"), h("quit")}
	}
	return []hint{h("expand"), lens, h("pull_all"), h("pull"), h("fetch"), h("cleanup"), h("cleanup_all"), h("search"), h("choose_owner"), pane, h("help")}
}

// hintLine draws the keys that work now; y is its screen line.
func (m *Model) hintLine(y int) drawn {
	parts := []cell{{text: " "}}
	x := 1
	for i, h := range m.hints() {
		if h.key == "" {
			continue
		}
		if i > 0 && len(parts) > 1 {
			parts = append(parts, cell{text: " · ", role: roleBorder})
			x += 3
		}
		cs := []cell{{text: h.key, role: roleAccent, bold: true}, {text: " " + h.does, role: roleMuted}}
		k := strings.Fields(h.key)[0]
		m.hits = append(m.hits, hit{y: y, x0: x, x1: x + width(cs), act: func(m *Model) tea.Cmd { return m.key(k) }})
		parts = append(parts, cs...)
		x += width(cs)
	}
	return drawn{parts: cut(parts, m.width)}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
