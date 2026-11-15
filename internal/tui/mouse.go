package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// hit is a place on the screen a click acts on.
type hit struct {
	y, x0, x1 int
	act       func(*Model) tea.Cmd
}

// click is the last click on the list, to tell a double click.
type click struct {
	y  int
	at time.Time
}

// doubleClick is how close two clicks on one row must be to open it.
const doubleClick = 400 * time.Millisecond

// mouse acts on a click or the wheel. The mouse only moves, opens and
// folds: whatever changes a clone or GitHub goes through a key and its
// confirm.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	mo := msg.Mouse()
	if m.popup != nil {
		return m.popupMouse(msg)
	}
	listEnd := listTop + m.listHeight()
	paneTop := listEnd + 1
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		d := 1
		if msg.Button == tea.MouseWheelUp {
			d = -1
		}
		switch {
		case mo.Y >= listTop && mo.Y < listEnd:
			m.move(d)
		case mo.Y >= paneTop && mo.Y < paneTop+m.ph+2:
			m.pscroll = max(0, m.pscroll+d)
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil
		}
		if mo.Y >= listTop && mo.Y < listEnd {
			i := m.offset + mo.Y - listTop
			if i >= len(m.lines) {
				return nil
			}
			now := m.now()
			again := m.lastClick.y == mo.Y && now.Sub(m.lastClick.at) < doubleClick && i == m.sel
			m.moveTo(i)
			m.lastClick = click{y: mo.Y, at: now}
			if again {
				m.lastClick = click{}
				m.toggle()
			}
			return nil
		}
		for _, h := range m.hits {
			if h.y == mo.Y && mo.X >= h.x0 && mo.X < h.x1 {
				return h.act(m)
			}
		}
	}
	return nil
}

// popupMouse moves through the popup and scrolls it with the wheel. A
// click on an item puts the cursor on it, and a click on the item the
// cursor is on chooses it, so that one stray click chooses nothing; a
// click on what is read does nothing, so a confirm never runs by mouse;
// a click outside the box closes it. Nothing reaches the screen under
// the popup (gh-desk ADR-053).
func (m *Model) popupMouse(msg tea.MouseMsg) tea.Cmd {
	p := m.popup
	mo := msg.Mouse()
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		d := 1
		if msg.Button == tea.MouseWheelUp {
			d = -1
		}
		switch {
		case p.list != nil:
			p.list.sel = max(0, min(len(p.list.shown())-1, p.list.sel+d))
		case p.body != nil:
			p.body.top = max(0, min(p.body.top+d, max(0, len(p.body.lines)-m.rows())))
		}
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil
		}
		lines := m.popupLines()
		row := mo.Y - p.top
		if row < 0 || row >= len(lines) || mo.X < p.x0 || mo.X >= p.x0+width(lines[0].parts) {
			m.popup = nil
			return nil
		}
		if p.list == nil || row >= len(p.lines) || p.lines[row] < 0 {
			return nil
		}
		i := p.lines[row]
		if i != p.list.sel {
			p.list.sel = i
			return nil
		}
		m.popup = nil
		return p.list.choose(m, p.list.shown()[i])
	}
	return nil
}
