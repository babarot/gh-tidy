package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/tui/columns"
	"github.com/babarot/gh-tidy/internal/tui/keymap"
	"github.com/babarot/gh-tidy/internal/tui/statusline"
)

// The width of the facts on the preview's right, and the lines around the
// list: the header, the column titles, the count, the preview's borders,
// the status line and the key line.
const (
	factsWidth = 50
	factsFrom  = 110 // narrower than this, the pane shows no facts
	allRepo    = 6   // what the repository column widens by in all, for the owner
	around     = 7
)

// The activity box: how long it stays after the run ends, and how many
// tasks it shows at the most. The rate limit stays rateFor after the last
// request to GitHub's API.
const (
	foldAfter = 3 * time.Second
	boxTasks  = 3
	rateFor   = 10 * time.Second
)

// filterRows is 1 while the filter line shows, above the list.
func (m *Model) filterRows() int {
	if m.filtering || m.filter != "" {
		return 1
	}
	return 0
}

// listTop is the screen line the list starts on, under the header, the
// filter and the column titles.
func (m *Model) listTop() int { return 2 + m.filterRows() }

// listHeight is how many lines the list shows. The activity box takes
// none: it is drawn over the preview, so that nothing moves as it comes
// and goes.
func (m *Model) listHeight() int {
	return max(1, m.height-m.ph-around-m.filterRows())
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
	a := m.eng.Activity()
	box, rateIn := m.activityBox(a)
	var ls []drawn
	ls = append(ls, m.header())
	if m.filterRows() > 0 {
		ls = append(ls, m.filterLine())
	}
	cols, ws := m.layout()
	ls = append(ls, m.titles(cols, ws))
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
	// The box takes the bottom of the preview, which closes above it: the
	// preview keeps its top and its two borders, and shows fewer lines.
	box = fitBox(box, m.ph)
	ls = append(ls, m.preview(len(ls), m.ph-len(box))...)
	ls = append(ls, box...)
	ls = append(ls, m.statusLine(a, rateIn), m.hintLine(len(ls)+1))
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
	return columns.Ctx{Owner: &m.snap, Symbols: m.sym, Now: m.now(), Roots: m.roots, Twins: m.twins, Home: m.home, OpenKey: m.keys.First("open")}
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
		r = append(r, cell{text: fmt.Sprintf("%d can pull", up), role: roleAccent}, dot,
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
		// What is being done to the clone has a light running over it.
		if c.Name == "main" && l.row != nil && l.row.Op.Doing != "" {
			content = m.shimmer(content)
		}
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

// filterLine shows the filter, being typed or kept.
func (m *Model) filterLine() drawn {
	parts := []cell{{text: " / ", role: roleAccent}, {text: m.filter, role: rolePrompt}}
	if m.filtering {
		parts = append(parts, cell{text: "▏", role: roleAccent})
	}
	return drawn{parts: cut(parts, m.width)}
}

// summary counts the rows.
func (m *Model) summary() drawn {
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
			p.Lines = []columns.Cell{{{Text: "  " + m.keys.First("open") + " opens it, " + m.keys.First("close") + " folds it, and so does a click", Role: roleDim}}}
		}
		return pane{parts: []columns.Part{p}}
	}
	it := l.item()
	p := pane{title: l.name(), facts: columns.Facts(x, it, m.home)}
	if l.kind == columns.KindWorktree {
		p.title = m.sym.Worktree + " " + l.name()
	}
	if m.lens == columns.GitHub || l.kind == columns.KindRemote {
		var d model.GitHubDetail
		if l.row != nil && l.row.Remote == m.ghdOf {
			d = m.ghd
		}
		p.parts = columns.GitHubParts(x, it, d)
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

// preview draws the pane below the list, rows lines of it between its
// borders; top is the screen line it starts on.
func (m *Model) preview(top, rows int) []drawn {
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
	m.pscroll = max(0, min(m.pscroll, max(0, len(lefts)-rows)))
	more := len(lefts) - m.pscroll - rows
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
	for i := range rows {
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

// statusLine says what was said last; the work being done is in the
// activity box. On the right is the rate limit, when it is shown and the
// box's head has no room for it.
func (m *Model) statusLine(a model.Activity, rateIn bool) drawn {
	left := []cell{{text: " "}}
	if msg, ok := m.line.Current(m.now()); ok {
		left = append(left, cell{text: msg.Text, role: kindRole[msg.Kind]})
	}
	var right []cell
	if m.rateShown(a) && !rateIn {
		right = append(m.rate(a, false), cell{text: " "})
	}
	gap := m.width - width(left) - width(right)
	if gap < 1 {
		return drawn{parts: cut(left, m.width)}
	}
	return drawn{parts: append(append(left, cell{text: strings.Repeat(" ", gap)}), right...)}
}

// boxShown reports whether the activity box shows: while a run goes on,
// and for a while after.
func (m *Model) boxShown(a model.Activity) bool {
	return a.Busy() || (!a.Ended.IsZero() && m.now().Sub(a.Ended) < foldAfter)
}

// rateShown reports whether the rate limit shows: while gh tidy asks
// GitHub's API, and for a while after.
func (m *Model) rateShown(a model.Activity) bool {
	r := a.Rate
	return r.Known && r.Limit > 0 && (r.Asking || (!r.LastAsked.IsZero() && m.now().Sub(r.LastAsked) < rateFor))
}

// rate is what GitHub's rate limit has left, the lower of REST's and
// GraphQL's, and what the load asked for spent and saved; compact leaves
// out when it resets.
func (m *Model) rate(a model.Activity, compact bool) []cell {
	r := a.Rate
	role := roleSubtle
	if r.Remaining*10 < r.Limit {
		role = roleWarn
	}
	dot := cell{text: " · ", role: roleBorder}
	parts := []cell{{text: "rate limit ", role: roleMuted}, {text: thousands(r.Remaining) + "/" + thousands(r.Limit), role: role}}
	if d := r.Reset.Sub(m.now()); !compact && d > 0 {
		parts = append(parts, cell{text: fmt.Sprintf(" resets in %dm", int((d+time.Minute-1)/time.Minute)), role: roleDim})
	}
	parts = append(parts, cell{text: "  │ ", role: roleBorder}, cell{text: "this load ", role: roleMuted},
		cell{text: fmt.Sprintf("%d used", r.Used), role: roleText}, dot, cell{text: fmt.Sprintf("%d saved", r.Saved), role: roleOK})
	if r.UnaskedUsed+r.UnaskedSaved > 0 {
		parts = append(parts, dot, cell{text: fmt.Sprintf("unasked %d used", r.UnaskedUsed), role: roleDim})
	}
	return parts
}

// pastVerbs say what a run did, once it ended.
var pastVerbs = map[string]string{"Reading": "Read", "Fetching": "Fetched", "Pulling": "Pulled", "Cleaning up": "Cleaned up", "Cloning": "Cloned"}

// taskKinds name each kind of task in the box: a read by where it reads
// (local, github), a change by what it does; taskDoing say what one
// running does.
var (
	taskKinds = map[string]string{"clone": "local", "github": "github", "fetch": "fetch", "pull": "pull", "cleanup": "cleanup", "create": "clone"}
	taskDoing = map[string]string{"clone": "reading", "github": "asking GitHub", "fetch": "fetching", "pull": "updating main",
		"cleanup": "cleaning up", "create": "cloning"}
)

// activityBox is the work of the run, after docker compose's: a head
// with the counts, the rate limit and the time, the workers when w asked
// for them, and the last tasks: the newest running ones, and above them
// the last done when fewer run than it shows. It is nil when
// the box does not show; rateIn says the rate limit is in its head.
func (m *Model) activityBox(a model.Activity) (box []drawn, rateIn bool) {
	if !m.boxShown(a) {
		return nil, false
	}
	w, now := m.width, m.now()
	busy := a.Busy()
	verb := a.Verb
	if !busy {
		verb = cmp.Or(pastVerbs[verb], verb)
	}
	var counts []string
	for _, c := range a.Counts {
		if c.Name == "GitHub" {
			counts = append(counts, fmt.Sprintf("GitHub %d/%d", c.Done, c.Total))
		} else {
			counts = append(counts, fmt.Sprintf("%d/%d %s", c.Done, c.Total, c.Name))
		}
	}
	head := []cell{{text: " [+] ", role: roleAccent, bold: true}, {text: strings.TrimSpace(verb+" "+a.What) + " ", role: roleStrong, bold: true},
		{text: strings.Join(counts, " · "), role: roleMuted}}
	if n := len(a.Running) - boxTasks; n > 0 {
		head = append(head, cell{text: fmt.Sprintf("  +%d running", n), role: roleAccent})
	}
	end := now
	if !busy {
		end = a.Ended
	}
	took := []cell{{text: "  " + seconds(end.Sub(a.Started)) + " ", role: roleDim}}
	var rate []cell
	if m.rateShown(a) {
		rate, rateIn = m.rate(a, true), true
	}
	gap := w - width(head) - width(rate) - width(took)
	if gap < 1 && rateIn {
		rate, rateIn = nil, false
		gap = w - width(head) - width(took)
	}
	if gap < 1 {
		box = append(box, drawn{parts: cut(head, w)})
	} else {
		box = append(box, drawn{parts: slices.Concat(head, []cell{{text: strings.Repeat(" ", gap)}}, rate, took)})
	}
	if m.workers && busy {
		box = append(box, drawn{parts: cut(append([]cell{{text: "     workers  ", role: roleDim}}, workersLine(a)...), w)})
	}
	shown := a.Running
	if len(shown) > boxTasks {
		shown = shown[len(shown)-boxTasks:]
	}
	if n := boxTasks - len(shown); n > 0 {
		shown = append(slices.Clone(a.Done[max(0, len(a.Done)-n):]), shown...)
	}
	for _, t := range shown {
		box = append(box, m.taskLine(t, now))
	}
	return box, rateIn
}

// fitBox keeps the box within n lines: its head and the workers' line,
// then the newest tasks.
func fitBox(box []drawn, n int) []drawn {
	if len(box) <= n {
		return box
	}
	keep := 1
	if len(box) > 1 && strings.Contains(plain(box[1]), "workers") {
		keep = 2
	}
	if n <= keep {
		return box[:max(0, n)]
	}
	return append(slices.Clone(box[:keep]), box[len(box)-(n-keep):]...)
}

// taskLine is one task in the activity box: how it is, its kind, its
// name, what it does or how it went, and how long it took.
func (m *Model) taskLine(t model.Task, now time.Time) drawn {
	icon := cell{text: " " + statusline.Spinner(now) + " ", role: roleAccent}
	status := cell{text: taskDoing[t.Kind], role: roleAccent}
	end := now
	switch {
	case t.End.IsZero():
	case t.Failed:
		icon, status, end = cell{text: " ✗ ", role: roleWarn}, cell{text: t.Note, role: roleWarn}, t.End
	default:
		icon, status, end = cell{text: " ✔ ", role: roleOK}, cell{text: cmp.Or(t.Note, "done"), role: roleMuted}, t.End
	}
	kind := cut([]cell{{text: cmp.Or(taskKinds[t.Kind], t.Kind), role: roleDim}}, 8)
	name := cut([]cell{{text: t.Name, role: roleText}}, 30)
	left := cut(slices.Concat([]cell{icon}, kind, name, []cell{status}), max(0, m.width-8))
	took := fmt.Sprintf("%7s ", seconds(end.Sub(t.Start)))
	return drawn{parts: cut(append(left, cell{text: took, role: roleDim}), m.width)}
}

// workersLine counts the work of each kind of outside access running,
// out of its limit, and the work waiting.
func workersLine(a model.Activity) []cell {
	var parts []cell
	for i, x := range a.Workers {
		if i > 0 {
			parts = append(parts, cell{text: " · ", role: roleBorder})
		}
		role := roleDim
		switch {
		case x.Running >= x.Limit:
			role = rolePrompt
		case x.Running > 0:
			role = roleAccent
		}
		parts = append(parts, cell{text: x.Name + " ", role: roleDim}, cell{text: fmt.Sprintf("%d/%d", x.Running, x.Limit), role: role})
	}
	role := roleDim
	if a.Queued > 0 {
		role = roleMuted
	}
	return append(parts, cell{text: " · ", role: roleBorder}, cell{text: fmt.Sprintf("%d queued", a.Queued), role: role})
}

// shimmer runs a light over cs, the muted text of what is being done:
// the text color on one character and the accent color on those beside
// it, brighter than the words in every theme. It moves one character each
// frame of the one clock (ADR-007), from before the text to past its end,
// and changes colors, never the text or its width.
func (m *Model) shimmer(cs []cell) []cell {
	const before, after, beside = 4, 8, 2
	var rs []rune
	var base []cell
	for _, c := range cs {
		for _, r := range c.text {
			rs = append(rs, r)
			base = append(base, c)
		}
	}
	if len(rs) == 0 {
		return cs
	}
	at := int(m.now().UnixMilli()/statusline.FrameEvery.Milliseconds())%(len(rs)+before+after) - before
	var out []cell
	for i, r := range rs {
		c := base[i]
		switch d := max(i-at, at-i); {
		case d == 0:
			c.role = roleText
		case d <= beside:
			c.role = roleAccent
		}
		c.text = string(r)
		if n := len(out); n > 0 && out[n-1].role == c.role && out[n-1].bold == c.bold && out[n-1].underline == c.underline {
			out[n-1].text += c.text
			continue
		}
		out = append(out, c)
	}
	return out
}

// seconds writes a duration as seconds with a tenth.
func seconds(d time.Duration) string { return fmt.Sprintf("%.1fs", max(0, d).Seconds()) }

// thousands writes n with commas.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// kindRole is the color of a message of each kind.
var kindRole = map[statusline.Kind]columns.Role{
	statusline.Info: roleMuted, statusline.Working: roleAccent, statusline.Done: roleOK,
	statusline.Refused: rolePrompt, statusline.Failed: roleWarn,
}

// hint is a key in the key line and what it does; changes says the key
// changes a clone or GitHub, which a click does not press.
type hint struct {
	key, does string
	changes   bool
}

// changes are the actions that change a clone, or ask GitHub for what it
// was not asked: the mouse only moves, opens and folds, so a click on
// their hints does nothing.
var changes = map[keymap.Action]bool{"fetch": true, "fetch_all": true, "pull": true, "pull_all": true, "cleanup": true,
	"cleanup_all": true, "clone": true, "refresh": true, "list": true}

func (m *Model) hints() []hint {
	if m.popup != nil {
		return m.popupHints(true)
	}
	if m.filtering {
		return []hint{{key: m.keys.First("accept"), does: m.keys.Help("accept")}, {key: "esc", does: "clear"}}
	}
	h := func(a keymap.Action) hint {
		return hint{key: m.keys.First(a), does: m.keys.Help(a), changes: changes[a]}
	}
	other := columns.Lenses[(slices.Index(columns.Lenses, m.lens)+1)%len(columns.Lenses)]
	lens := hint{key: m.keys.First("lens"), does: string(other)}
	pane := hint{key: strings.TrimSpace(m.keys.First("preview_grow") + " " + m.keys.First("preview_shrink")), does: "preview"}
	if m.lens == columns.GitHub {
		return []hint{lens, h("open_browser"), h("clone"), h("search"), h("choose_owner"), h("help"), h("quit")}
	}
	open := hint{key: strings.TrimSpace(m.keys.First("open") + " " + m.keys.First("close")), does: "open / close"}
	return []hint{open, lens, h("pull_all"), h("pull"), h("fetch"), h("cleanup"), h("cleanup_all"), h("search"), h("choose_owner"), pane, h("help")}
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
		if k := strings.Fields(h.key)[0]; !h.changes {
			m.hits = append(m.hits, hit{y: y, x0: x, x1: x + width(cs), act: func(m *Model) tea.Cmd { return m.key(k) }})
		}
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
