package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/theme"
	"github.com/babarot/gh-tidy/internal/tui/columns"
)

// The roles of the screen are the columns' roles: one set of names for
// every color, which the theme gives values.
const (
	roleText     = columns.Text
	roleMuted    = columns.Muted
	roleSubtle   = columns.Subtle
	roleStrong   = columns.Strong
	roleAccent   = columns.Accent
	roleTitle    = columns.Title
	roleOK       = columns.OK
	rolePrompt   = columns.Prompt
	roleWorktree = columns.Worktree
	roleWarn     = columns.Warn
	roleDim      = columns.Dim
	roleBorder   = columns.Border
)

// styles are the colors of one palette, by role.
type styles struct {
	fg      map[columns.Role]color.Color
	surface color.Color
	faint   color.Color // what a popup dims the screen to
}

func newStyles(p theme.Palette) styles {
	c := func(s string) color.Color { return lipgloss.Color(s) }
	return styles{
		fg: map[columns.Role]color.Color{
			roleText: c(p.Text), roleMuted: c(p.Muted), roleSubtle: c(p.Subtle), roleStrong: c(p.Strong),
			roleAccent: c(p.Accent), roleTitle: c(p.Title), roleOK: c(p.OK), rolePrompt: c(p.Prompt),
			roleWorktree: c(p.Worktree), roleWarn: c(p.Warn), roleDim: c(p.Dim), roleBorder: c(p.Border),
		},
		surface: c(p.Surface),
		faint:   c(p.Border),
	}
}

// cell is a part of a line as drawn: text, color, and how.
type cell struct {
	text      string
	role      columns.Role
	bold      bool
	underline bool
}

// drawn is one line of the screen, in parts; bg paints its background.
type drawn struct {
	parts []cell
	bg    bool
}

func fromCell(c columns.Cell) []cell {
	out := make([]cell, len(c))
	for i, s := range c {
		out[i] = cell{text: s.Text, role: s.Role, bold: s.Bold, underline: s.Underline}
	}
	return out
}

func width(cs []cell) int {
	n := 0
	for _, c := range cs {
		n += ansi.StringWidth(c.text)
	}
	return n
}

// cut fits parts in w cells: cut with … when longer, padded when shorter.
func cut(cs []cell, w int) []cell {
	var out []cell
	used := 0
	for _, c := range cs {
		if used >= w {
			break
		}
		n := ansi.StringWidth(c.text)
		if used+n > w {
			c.text = ansi.Truncate(c.text, w-used, "…")
			n = ansi.StringWidth(c.text)
		}
		out = append(out, c)
		used += n
	}
	if used < w {
		out = append(out, cell{text: strings.Repeat(" ", w-used)})
	}
	return out
}

// paint writes a line with its colors; dim paints it all faint, as under
// a popup.
func (s styles) paint(d drawn, dim bool) string {
	var b strings.Builder
	for _, c := range d.parts {
		st := lipgloss.NewStyle().Foreground(s.fg[c.role])
		if dim {
			st = lipgloss.NewStyle().Foreground(s.faint)
		} else {
			st = st.Bold(c.bold).Underline(c.underline)
		}
		if d.bg && !dim {
			st = st.Background(s.surface)
		}
		b.WriteString(st.Render(c.text))
	}
	return b.String()
}

// plain is a line's text without colors, for tests and for overlaying.
func plain(d drawn) string {
	var b strings.Builder
	for _, c := range d.parts {
		b.WriteString(c.text)
	}
	return b.String()
}
