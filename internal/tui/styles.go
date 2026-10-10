package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/theme"
)

// styles holds every style the TUI renders with, built from one palette. The
// layout is claude-recall's: a header bar, ╌ rules around the column headers,
// an accent bar on the selected row and a rounded detail pane.
type styles struct {
	surface  lipgloss.Style // background of the header bar and the selected row
	app      lipgloss.Style
	tag      lipgloss.Style
	rule     lipgloss.Style
	colHdr   lipgloss.Style
	bar      lipgloss.Style
	name     lipgloss.Style // repository names
	text     lipgloss.Style
	strong   lipgloss.Style
	subtle   lipgloss.Style
	muted    lipgloss.Style
	dim      lipgloss.Style
	accent   lipgloss.Style // behind, BASE, keys
	worktree lipgloss.Style // ⌥, ahead, unpushed
	ok       lipgloss.Style // staged, confirmations
	warn     lipgloss.Style // diverged, no origin, errors
	prompt   lipgloss.Style // unstaged, stale, the filter
	stash    lipgloss.Style
	key      lipgloss.Style
	border   color.Color
	tabOn    lipgloss.Style
}

func fg(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }

func newStyles(p theme.Palette) styles {
	return styles{
		surface:  lipgloss.NewStyle().Background(lipgloss.Color(p.Surface)),
		app:      fg(p.Accent).Bold(true),
		tag:      fg(p.Dim),
		rule:     fg(p.Border),
		colHdr:   fg(p.Strong).Bold(true),
		bar:      fg(p.Accent),
		name:     fg(p.Title).Bold(true),
		text:     fg(p.Text),
		strong:   fg(p.Strong),
		subtle:   fg(p.Subtle),
		muted:    fg(p.Muted),
		dim:      fg(p.Dim),
		accent:   fg(p.Accent),
		worktree: fg(p.Worktree),
		ok:       fg(p.OK),
		warn:     fg(p.Warn),
		prompt:   fg(p.Prompt),
		stash:    fg(p.Title),
		key:      fg(p.Accent).Bold(true),
		border:   lipgloss.Color(p.Border),
		tabOn:    fg(p.Accent).Bold(true).Underline(true),
	}
}

// seg is a run of text in one style. Cells are built from segs so they can
// be cut to a width before styling, and take the selected row's background.
type seg struct {
	text  string
	style lipgloss.Style
}

type segs []seg

func (s segs) width() int {
	n := 0
	for _, x := range s {
		n += ansi.StringWidth(x.text)
	}
	return n
}

// add appends text in style, with a space before it when s is not empty.
func (s segs) add(text string, style lipgloss.Style) segs {
	if text == "" {
		return s
	}
	if len(s) > 0 {
		s = append(s, seg{" ", lipgloss.NewStyle()})
	}
	return append(s, seg{text, style})
}

// join appends more after a space, when there is anything to append.
func (s segs) join(more segs) segs {
	if len(more) == 0 {
		return s
	}
	if len(s) > 0 {
		s = append(s, seg{" ", lipgloss.NewStyle()})
	}
	return append(s, more...)
}

// render draws s in exactly w cells, cut with … when longer, on bg when
// it has a background.
func (s segs) render(w int, bg *lipgloss.Style) string {
	var b strings.Builder
	left := w
	for i, x := range s {
		if left <= 0 {
			break
		}
		t := x.text
		tw := ansi.StringWidth(t)
		rest := s[i+1:].width()
		if tw > left || (tw == left && rest > 0) {
			t = ansi.Truncate(t, left, "…")
			tw = ansi.StringWidth(t)
			left = 0
		} else {
			left -= tw
		}
		st := x.style
		if bg != nil {
			st = st.Background(bg.GetBackground())
		}
		b.WriteString(st.Render(t))
		if left == 0 {
			break
		}
	}
	if left > 0 {
		pad := lipgloss.NewStyle()
		if bg != nil {
			pad = *bg
		}
		b.WriteString(pad.Render(strings.Repeat(" ", left)))
	}
	return b.String()
}
