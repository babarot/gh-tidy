package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/tui/keymap"
)

// popup is the one popup shown over the list: a list to choose from, which
// typing narrows, or a body to read. The keys it reads are the same in
// every popup; esc closes it.
type popup struct {
	title string
	width int

	// A list to choose from: items, narrowed by query, with the cursor at
	// sel among those shown; choose takes the item chosen.
	items  []string
	query  string
	sel    int
	choose func(m *Model, item string) tea.Cmd

	// A body to read, scrolled by top.
	body []drawn
	top  int

	// confirm runs what the popup asks about, on the confirm keys.
	confirm func(m *Model) tea.Cmd

	hints []hint
	// rows are where the popup is on the screen, for the mouse: the first
	// line of its items or body, and its left edge.
	y0, x0 int
}

// shown are the items the query leaves.
func (p *popup) shown() []string {
	if p.query == "" {
		return p.items
	}
	var out []string
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it), strings.ToLower(p.query)) {
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) ownerPopup() *popup {
	p := &popup{title: "owner", width: 44, items: m.owners,
		choose: func(m *Model, o string) tea.Cmd { m.openOwner(o); return nil },
		hints:  []hint{{m.keys.First("choose"), m.keys.Help("choose")}, {"type", "narrow"}, {"esc", "close"}}}
	for i, o := range m.owners {
		if strings.EqualFold(o, m.owner) {
			p.sel = i
		}
	}
	return p
}

// confirmPopup asks before a change: the confirm keys run it, esc does not.
func (m *Model) confirmPopup(title string, body []drawn, run func(*Model) tea.Cmd) *popup {
	body = append(body, drawn{}, drawn{parts: []cell{{text: m.keys.First("confirm"), role: roleAccent, bold: true}, {text: " " + m.keys.Help("confirm"), role: rolePrompt},
		{text: " · ", role: roleBorder}, {text: "esc", role: roleAccent, bold: true}, {text: " cancel", role: roleMuted}}})
	return &popup{title: title, width: 100, body: body, confirm: run,
		hints: []hint{{m.keys.First("confirm"), m.keys.Help("confirm")}, {"esc", "cancel"}}}
}

func (m *Model) helpPopup() *popup {
	p := &popup{title: "keys", width: 64, hints: []hint{{"esc", "close"}}}
	add := func(keys, does string) {
		p.body = append(p.body, drawn{parts: append(cut([]cell{{text: keys, role: roleAccent, bold: true}}, 18), cell{text: does, role: roleText})})
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
	p.body = append(p.body, drawn{}, drawn{parts: []cell{{text: "Marks: " + m.sym.Behind + " behind  " + m.sym.Ahead + " ahead  " + m.sym.BehindMain +
		" behind the default branch  " + m.sym.GitHubAhead + " GitHub is ahead", role: roleMuted}}},
		drawn{parts: []cell{{text: "       " + m.sym.Unstaged + " unstaged  " + m.sym.Staged + " staged  " + m.sym.Untracked + " untracked  " +
			m.sym.Stash + " stash  " + m.sym.Worktree + " worktrees  ⋅ not read yet", role: roleMuted}}})
	return p
}

// popupKey gives a key to the popup.
func (m *Model) popupKey(k string) tea.Cmd {
	p := m.popup
	on := func(a keymap.Action) bool { return m.keys.Matches(a, k) }
	if k == keymap.Back {
		if p.query != "" {
			p.query, p.sel = "", 0
			return nil
		}
		m.popup = nil
		return nil
	}
	if p.confirm != nil && on("confirm") {
		m.popup = nil
		return p.confirm(m)
	}
	if p.body != nil {
		switch {
		case on("up"):
			p.top = max(0, p.top-1)
		case on("down"):
			p.top++
		case on("page_up"):
			p.top = max(0, p.top-10)
		case on("page_down"):
			p.top += 10
		case on("help"), on("quit"):
			m.popup = nil
		}
		return nil
	}
	items := p.shown()
	switch {
	case on("choose"):
		if p.sel < len(items) {
			m.popup = nil
			return p.choose(m, items[p.sel])
		}
	case k == "up" || k == "ctrl+p":
		p.sel = max(0, p.sel-1)
	case k == "down" || k == "ctrl+n":
		p.sel = min(len(items)-1, p.sel+1)
	case k == "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query, p.sel = string(r[:len(r)-1]), 0
		}
	case len([]rune(k)) == 1:
		p.query, p.sel = p.query+k, 0
	}
	return nil
}

// popupLines draws the popup's box.
func (m *Model) popupLines() []drawn {
	p := m.popup
	w := min(p.width, m.width-4)
	inner := w - 4
	var body []drawn
	maxRows := max(3, m.height/2)
	if p.body != nil {
		p.top = max(0, min(p.top, max(0, len(p.body)-maxRows)))
		body = p.body[p.top:min(len(p.body), p.top+maxRows)]
	} else {
		if p.query != "" || len(p.items) > 8 {
			body = append(body, drawn{parts: []cell{{text: "/ ", role: roleAccent}, {text: p.query, role: rolePrompt}, {text: "▏", role: roleAccent}}})
		}
		items := p.shown()
		p.sel = max(0, min(p.sel, len(items)-1))
		from := max(0, p.sel-maxRows+1)
		for i := from; i < min(len(items), from+maxRows); i++ {
			if i == p.sel {
				body = append(body, drawn{parts: []cell{{text: "▎ ", role: roleAccent}, {text: items[i], role: roleStrong, bold: true}}})
				continue
			}
			body = append(body, drawn{parts: []cell{{text: "  "}, {text: items[i], role: roleText}}})
		}
		if len(items) == 0 {
			body = append(body, drawn{parts: []cell{{text: "  nothing matches", role: roleDim}}})
		}
	}
	title := "─ " + p.title + " "
	out := []drawn{{parts: []cell{{text: "╭" + title + strings.Repeat("─", max(0, w-2-ansi.StringWidth(title))) + "╮", role: roleAccent}}}}
	for _, b := range body {
		out = append(out, drawn{parts: append(append([]cell{{text: "│ ", role: roleAccent}}, cut(b.parts, inner)...), cell{text: " │", role: roleAccent})})
	}
	return append(out, drawn{parts: []cell{{text: "╰" + strings.Repeat("─", w-2) + "╯", role: roleAccent}}})
}

// overlay draws the popup over the screen, dimmed beneath it.
func (m *Model) overlay(ls []drawn) []string {
	box := m.popupLines()
	top := max(0, m.height/4-2)
	left := max(0, (m.width-width(box[0].parts))/2)
	m.popup.y0, m.popup.x0 = top+1, left
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = m.st.paint(l, true)
		if j := i - top; j >= 0 && j < len(box) {
			text := plain(l)
			before := ansi.Cut(text, 0, left)
			after := ansi.Cut(text, left+width(box[j].parts), m.width)
			out[i] = m.st.paint(drawn{parts: []cell{{text: before}}}, true) + m.st.paint(box[j], false) + m.st.paint(drawn{parts: []cell{{text: after}}}, true)
		}
	}
	return out
}
