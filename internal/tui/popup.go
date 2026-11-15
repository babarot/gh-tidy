package tui

// The popup layer (ADR-006): a popup declares the parts it holds (a list to choose from, a body to
// read, a question to confirm), and this layer reads the keys and the
// mouse the same way for every popup, and writes its hints from the
// keymap. One popup shows at a time; esc steps back one level; the key
// that opened a popup does not close it, and quit is not read in one.

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/tui/keymap"
)

// The popup's widths: a narrow one for a list, a wide one for what is
// read. Constants: they set the layout, which is not a setting.
const (
	popupNarrow = 64
	popupWide   = 100
)

// popup is the one popup shown over the list.
type popup struct {
	title string
	wide  bool

	list     *popupList
	body     *popupBody
	question *popupQuestion

	// top is the screen line the box starts on, set when the popup opens
	// and when the terminal is resized, and held meanwhile, so that the
	// box does not jump as what it holds changes. x0 is its left edge.
	top, x0 int
	// lines are the item of each line of the box, for the mouse; -1 for
	// a line that is not an item.
	lines []int
}

// popupList is a list to choose from. Typing narrows it when filter is
// set.
type popupList struct {
	items  []string
	filter bool
	query  string
	sel    int
	// top is the first item shown. It moves only when the cursor leaves
	// what is shown, so that a click never moves the items under the
	// mouse.
	top    int
	choose func(m *Model, item string) tea.Cmd
}

// shown are the items the query leaves.
func (l *popupList) shown() []string {
	if l.query == "" {
		return l.items
	}
	var out []string
	for _, it := range l.items {
		if strings.Contains(strings.ToLower(it), strings.ToLower(l.query)) {
			out = append(out, it)
		}
	}
	return out
}

// showsQuery reports whether the list shows the line typed into.
func (l *popupList) showsQuery() bool { return l.filter && (l.query != "" || len(l.items) > 8) }

// popupBody is text to read, scrolled by top.
type popupBody struct {
	lines []drawn
	top   int
}

// popupQuestion is what the popup asks before a change: the confirm keys
// run it.
type popupQuestion struct {
	run func(m *Model) tea.Cmd
}

// open shows p, in place of any popup shown.
func (m *Model) open(p *popup) {
	m.popup = p
	m.place()
}

// place sets where the popup's box starts: a quarter down the screen, or
// in the middle when it is too tall for that.
func (m *Model) place() {
	p := m.popup
	if p == nil {
		return
	}
	h := 2 + p.naturalRows() + len(m.popupFoot())
	top := max(0, m.height/4-2)
	if top+h > m.height-1 {
		top = max(0, (m.height-h)/2)
	}
	p.top = top
}

// naturalRows is how many lines what the popup holds would take, unclipped.
func (p *popup) naturalRows() int {
	switch {
	case p.list != nil:
		n := len(p.list.shown())
		if p.list.showsQuery() {
			n++
		}
		return max(1, n)
	case p.body != nil:
		return len(p.body.lines)
	}
	return 0
}

// rows is how many lines of its list or body the popup shows: what fits
// below its top, less its borders and foot.
func (m *Model) rows() int {
	return max(1, m.height-m.popup.top-2-len(m.popupFoot())-1)
}

func (m *Model) ownerPopup() *popup {
	l := &popupList{items: m.owners, filter: true,
		choose: func(m *Model, o string) tea.Cmd { m.openOwner(o); return nil }}
	for i, o := range m.owners {
		if strings.EqualFold(o, m.owner) {
			l.sel = i
		}
	}
	return &popup{title: "owner", list: l}
}

// confirmPopup asks before a change: the confirm keys run it, esc does not.
func (m *Model) confirmPopup(title string, body []drawn, run func(*Model) tea.Cmd) *popup {
	return &popup{title: title, wide: true, body: &popupBody{lines: body}, question: &popupQuestion{run: run}}
}

func (m *Model) helpPopup() *popup {
	var lines []drawn
	add := func(keys, does string) {
		lines = append(lines, drawn{parts: append(cut([]cell{{text: keys, role: roleAccent, bold: true}}, 18), cell{text: does, role: roleText})})
	}
	for _, s := range []keymap.Scope{keymap.ScopeGlobal, keymap.ScopeNav, keymap.ScopeList} {
		for _, a := range m.keys.Actions(s) {
			ks := m.keys.Keys(a)
			if len(ks) == 0 {
				continue
			}
			add(strings.Join(ks, " "), m.keys.Help(a))
		}
	}
	add(keymap.Back, keymap.Fixed[keymap.Back])
	add(keymap.Interrupt, keymap.Fixed[keymap.Interrupt])
	lines = append(lines, drawn{}, drawn{parts: []cell{{text: "Marks: " + m.sym.Behind + " behind  " + m.sym.Ahead + " ahead  " + m.sym.BehindMain +
		" behind the default branch  " + m.sym.GitHubAhead + " GitHub is ahead", role: roleMuted}}},
		drawn{parts: []cell{{text: "       " + m.sym.Unstaged + " unstaged  " + m.sym.Staged + " staged  " + m.sym.Untracked + " untracked  " +
			m.sym.Stash + " stash  " + m.sym.Worktree + " worktrees  ⋅ not read yet", role: roleMuted}}})
	return &popup{title: "keys", body: &popupBody{lines: lines}}
}

// popupHints are the popup's keys, from what it holds and the keymap.
// writes keeps only the keys that change something, and esc, for the key
// line under the popup.
func (m *Model) popupHints(writes bool) []hint {
	p := m.popup
	var hs []hint
	add := func(a keymap.Action, write bool) {
		if k := m.keys.First(a); k != "" && (write || !writes) {
			hs = append(hs, hint{key: k, does: m.keys.Help(a)})
		}
	}
	switch {
	case p.question != nil:
		add("confirm", true)
	case p.list != nil:
		add("choose", false)
		if p.list.filter && !writes {
			hs = append(hs, hint{key: "type", does: "narrow"})
		}
	}
	back := "close"
	if p.question != nil {
		back = "cancel"
	}
	return append(hs, hint{key: keymap.Back, does: back})
}

// popupFoot are the lines under what the popup holds, which do not
// scroll: its keys.
func (m *Model) popupFoot() []drawn {
	var parts []cell
	for i, h := range m.popupHints(false) {
		if i > 0 {
			parts = append(parts, cell{text: " · ", role: roleBorder})
		}
		role := roleMuted
		if m.popup.question != nil && i == 0 {
			role = rolePrompt
		}
		parts = append(parts, cell{text: h.key, role: roleAccent, bold: true}, cell{text: " " + h.does, role: role})
	}
	return []drawn{{}, {parts: parts}}
}

// typing reports whether a key is one a list's filter takes as text: a
// character with no modifier. The nav keys that are such characters (j,
// k) type instead of moving while the list filters.
func typing(k string) bool {
	return utf8.RuneCountInString(k) == 1
}

// popupKey gives a key to the popup: esc steps back, then the question,
// then moving through the list or the body, then typing into the filter.
func (m *Model) popupKey(k string) tea.Cmd {
	p := m.popup
	on := func(a keymap.Action) bool { return m.keys.Matches(a, k) }
	if k == keymap.Back {
		if p.list != nil && p.list.query != "" {
			p.list.query, p.list.sel = "", 0
			return nil
		}
		m.popup = nil
		return nil
	}
	if p.question != nil && on("confirm") {
		m.popup = nil
		return p.question.run(m)
	}
	filtering := p.list != nil && p.list.filter
	nav := func(a keymap.Action) bool {
		return on(a) && (!filtering || !typing(k))
	}
	page := m.rows()
	switch {
	case p.list != nil:
		l := p.list
		n := len(l.shown())
		switch {
		case on("choose"):
			if l.sel >= 0 && l.sel < n {
				m.popup = nil
				return l.choose(m, l.shown()[l.sel])
			}
		case nav("up"):
			l.sel = max(0, l.sel-1)
		case nav("down"):
			l.sel = max(0, min(n-1, l.sel+1))
		case nav("top"):
			l.sel = 0
		case nav("bottom"):
			l.sel = max(0, n-1)
		case nav("page_up"):
			l.sel = max(0, l.sel-page)
		case nav("page_down"):
			l.sel = max(0, min(n-1, l.sel+page))
		case l.filter && on("erase"):
			if r := []rune(l.query); len(r) > 0 {
				l.query, l.sel = string(r[:len(r)-1]), 0
			}
		case l.filter && typing(k):
			l.query, l.sel = l.query+k, 0
		}
	case p.body != nil:
		b := p.body
		switch {
		case nav("up"):
			b.top--
		case nav("down"):
			b.top++
		case nav("top"):
			b.top = 0
		case nav("bottom"):
			b.top = len(b.lines)
		case nav("page_up"):
			b.top -= page
		case nav("page_down"):
			b.top += page
		}
		b.top = max(0, min(b.top, max(0, len(b.lines)-page)))
	}
	return nil
}

// popupLines draws the popup's box, and records which item each line is.
func (m *Model) popupLines() []drawn {
	p := m.popup
	w := popupNarrow
	if p.wide {
		w = popupWide
	}
	w = min(w, m.width-4)
	inner := w - 4
	rows := m.rows()
	var content []drawn
	var items []int
	above, below := 0, 0
	switch {
	case p.list != nil:
		l := p.list
		if l.showsQuery() {
			content = append(content, drawn{parts: []cell{{text: "/ ", role: roleAccent}, {text: l.query, role: rolePrompt}, {text: "▏", role: roleAccent}}})
			items = append(items, -1)
		}
		shown := l.shown()
		l.sel = max(0, min(l.sel, len(shown)-1))
		n := max(1, rows-len(content))
		if l.sel < l.top {
			l.top = l.sel
		}
		if l.sel >= l.top+n {
			l.top = l.sel - n + 1
		}
		l.top = max(0, min(l.top, max(0, len(shown)-n)))
		from := l.top
		for i := from; i < min(len(shown), from+n); i++ {
			if i == l.sel {
				content = append(content, drawn{parts: []cell{{text: "▎ ", role: roleAccent}, {text: shown[i], role: roleStrong, bold: true}}})
			} else {
				content = append(content, drawn{parts: []cell{{text: "  "}, {text: shown[i], role: roleText}}})
			}
			items = append(items, i)
		}
		if len(shown) == 0 {
			content = append(content, drawn{parts: []cell{{text: "  nothing matches", role: roleDim}}})
			items = append(items, -1)
		}
		above, below = from, max(0, len(shown)-from-n)
	case p.body != nil:
		b := p.body
		b.top = max(0, min(b.top, max(0, len(b.lines)-rows)))
		// A copy: the foot is appended below, and must not write over the
		// lines of the body past what is shown.
		content = slices.Clone(b.lines[b.top:min(len(b.lines), b.top+rows)])
		above, below = b.top, len(b.lines)-b.top-len(content)
		for range content {
			items = append(items, -1)
		}
	}
	title := "─ " + p.title + " "
	out := []drawn{{parts: []cell{{text: "╭" + title + strings.Repeat("─", max(0, w-2-ansi.StringWidth(title))) + "╮", role: roleAccent}}}}
	p.lines = []int{-1}
	for i, b := range append(content, m.popupFoot()...) {
		out = append(out, drawn{parts: append(append([]cell{{text: "│ ", role: roleAccent}}, cut(b.parts, inner)...), cell{text: " │", role: roleAccent})})
		if i < len(items) {
			p.lines = append(p.lines, items[i])
		} else {
			p.lines = append(p.lines, -1)
		}
	}
	var tail []string
	if above > 0 {
		tail = append(tail, fmt.Sprintf("↑ %d above", above))
	}
	if below > 0 {
		tail = append(tail, fmt.Sprintf("↓ %d more", below))
	}
	edge := ""
	if len(tail) > 0 {
		edge = " " + strings.Join(tail, " · ") + " "
	}
	p.lines = append(p.lines, -1)
	return append(out, drawn{parts: []cell{{text: "╰" + strings.Repeat("─", max(0, w-2-ansi.StringWidth(edge))) + edge + "╯", role: roleAccent}}})
}

// overlay draws the popup over the screen, dimmed beneath it.
func (m *Model) overlay(ls []drawn) []string {
	box := m.popupLines()
	top := m.popup.top
	left := max(0, (m.width-width(box[0].parts))/2)
	m.popup.x0 = left
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = m.st.paint(l, true)
		if j := i - top; j >= 0 && j < len(box) {
			// Cut by cells: a character two cells wide across an edge is
			// replaced by spaces, so that the box starts and ends where it
			// should.
			text := plain(l)
			bw := width(box[j].parts)
			before := fitCells(ansi.Cut(text, 0, left), left)
			after := fitCells(ansi.Cut(text, left+bw, m.width), max(0, m.width-left-bw))
			out[i] = m.st.paint(drawn{parts: []cell{{text: before}}}, true) + m.st.paint(box[j], false) + m.st.paint(drawn{parts: []cell{{text: after}}}, true)
		}
	}
	return out
}

// fitCells makes s exactly w cells wide: cut when longer, padded when
// shorter.
func fitCells(s string, w int) string {
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
