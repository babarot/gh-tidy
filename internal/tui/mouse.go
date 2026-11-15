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

// popupMouse moves through the popup and chooses with a click; a click
// outside it closes it.
func (m *Model) popupMouse(msg tea.MouseMsg) tea.Cmd {
	p := m.popup
	mo := msg.Mouse()
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		d := 1
		if msg.Button == tea.MouseWheelUp {
			d = -1
		}
		if p.body != nil {
			p.top = max(0, p.top+d)
		} else {
			p.sel = max(0, min(len(p.shown())-1, p.sel+d))
		}
	case tea.MouseClickMsg:
		lines := m.popupLines()
		if mo.Y < p.y0-1 || mo.Y >= p.y0-1+len(lines) || mo.X < p.x0 || mo.X >= p.x0+width(lines[0].parts) {
			m.popup = nil
			return nil
		}
		if p.body != nil {
			return nil
		}
		row := mo.Y - p.y0
		if p.query != "" || len(p.items) > 8 {
			row-- // the query line
		}
		items := p.shown()
		from := max(0, p.sel-m.popupRows()+1)
		if i := from + row; row >= 0 && i < len(items) {
			m.popup = nil
			return p.choose(m, items[i])
		}
	}
	return nil
}
