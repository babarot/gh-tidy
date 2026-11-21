// Package columns holds the columns of the list and the sections of the
// preview pane, each as one entry of a table: its name, its title, its
// width, what it needs read, and the function that writes it from a copy
// of an owner. A column is added, removed or moved by changing one entry;
// the config file picks which are shown and in what order.
//
// Nothing here draws: a cell is text in parts, each with the role of its
// color, and the TUI paints the roles with the theme.
package columns

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/model"
)

// Role is what a part of a cell is, which the theme gives a color.
type Role int

const (
	Text Role = iota
	Muted
	Subtle
	Strong
	Accent
	Title
	OK
	Prompt
	Worktree
	Warn
	Dim
	Border
)

// Seg is a part of a cell.
type Seg struct {
	Text      string
	Role      Role
	Bold      bool
	Underline bool
}

// Cell is a cell's text, in parts.
type Cell []Seg

// Lens is a set of columns, which v switches between.
type Lens string

const (
	Local  Lens = "local"
	GitHub Lens = "github"
)

// Lenses are the sets, in the order v goes through them.
var Lenses = []Lens{Local, GitHub}

// Kind is what a row of the list is.
type Kind int

const (
	KindClone    Kind = iota // a clone, with or without a repository on GitHub
	KindWorktree             // a linked worktree of the clone above it
	KindRemote               // a repository not cloned
	KindHead                 // the head of a section that folds
)

// Item is a row of the list as the columns see it.
type Item struct {
	Kind Kind
	Row  *model.Row      // nil for a head
	Tree *model.Worktree // the worktree, for KindWorktree
}

// Ctx is what a cell is written with besides its row.
type Ctx struct {
	Owner   *model.Owner // All writes the owner before each name
	Symbols config.Marks
	Now     time.Time
	// Roots are where clones live, and Twins the clones whose name
	// another clone shares (Twins): those say which root they are in.
	Roots []string
	Twins map[string]bool
	// Home is written as ~ in paths.
	Home string
}

// Short writes p with ~ for the home directory.
func (x Ctx) Short(p string) string {
	if x.Home != "" && strings.HasPrefix(p, x.Home+"/") {
		return "~" + p[len(x.Home):]
	}
	return p
}

// twin is what two clones of one name share: their owner's directory and
// their own name.
func twin(clone model.CloneKey) string {
	p := string(clone)
	return strings.ToLower(filepath.Base(filepath.Dir(p)) + "/" + filepath.Base(p))
}

// Twins are the clones of rows whose owner and name another clone
// shares, as in two roots.
func Twins(rows []model.Row) map[string]bool {
	n := map[string]int{}
	for _, r := range rows {
		if r.Clone != "" {
			n[twin(r.Clone)]++
		}
	}
	out := map[string]bool{}
	for k, c := range n {
		if c > 1 {
			out[k] = true
		}
	}
	return out
}

// RootLabel names the root path is under by its last directories: one,
// or as many as tell it from the other roots.
func RootLabel(roots []string, path string) string {
	var in string
	for _, r := range roots {
		if strings.HasPrefix(path, r+string(filepath.Separator)) {
			in = r
		}
	}
	if in == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(in, string(filepath.Separator)), string(filepath.Separator))
	for n := 1; n <= len(parts); n++ {
		label := strings.Join(parts[len(parts)-n:], "/")
		unique := true
		for _, r := range roots {
			rp := strings.Split(strings.Trim(r, string(filepath.Separator)), string(filepath.Separator))
			if r != in && len(rp) >= n && strings.Join(rp[len(rp)-n:], "/") == label {
				unique = false
			}
		}
		if unique {
			return label
		}
	}
	return in
}

// Column is one column of the list.
type Column struct {
	Name  string
	Title string
	// Width is the column's width in cells; 0 takes what the others
	// leave, at least MinFlex.
	Width int
	// Drop is when the column is left out of a narrow terminal: the
	// highest first; 0 is never.
	Drop int
	Lens []Lens
	// Need is what the column needs read from GitHub.
	Need model.Need
	// Cell writes the column of an item; a worktree row and a repository
	// not cloned may leave it empty.
	Cell func(Ctx, Item) Cell
}

// MinFlex is the narrowest a column of Width 0 gets.
const MinFlex = 6

// Gap is the space between two columns.
const Gap = 2

// All are every column, in no order: the config file sets the order.
var All = []Column{
	{"repo", "REPO", 26, 0, []Lens{Local, GitHub}, 0, repoCell},
	{"main", "MAIN", 14, 0, []Lens{Local, GitHub}, 0, mainCell},
	{"checked_out", "CHECKED OUT", 28, 2, []Lens{Local}, 0, checkedOutCell},
	{"changes", "CHANGES", 14, 3, []Lens{Local}, 0, changesCell},
	{"worktrees", "WORKTREES", 0, 1, []Lens{Local}, 0, worktreesCell},
	{"fetched", "FETCHED", 9, 4, []Lens{Local}, 0, fetchedCell},
	{"issues", "ISSUES", 8, 2, []Lens{GitHub}, model.NeedCounts | model.NeedNewest, issuesCell},
	{"prs", "PRS", 8, 2, []Lens{GitHub}, model.NeedCounts | model.NeedNewest, prsCell},
	{"dependabot", "DEPENDABOT", 10, 3, []Lens{GitHub}, model.NeedAlerts, dependabotCell},
	{"ci", "CI", 4, 4, []Lens{GitHub}, model.NeedCI, ciCell},
	{"pushed", "PUSHED", 9, 5, []Lens{GitHub}, 0, pushedCell},
	{"stars", "STARS", 6, 5, []Lens{GitHub}, 0, starsCell},
	{"visibility", "", 7, 6, []Lens{GitHub}, 0, visibilityCell},
}

// Find returns the column named name.
func Find(name string) (Column, bool) {
	i := slices.IndexFunc(All, func(c Column) bool { return c.Name == name })
	if i < 0 {
		return Column{}, false
	}
	return All[i], true
}

// Names are the names of the columns of lens, for a message.
func Names(lens Lens) []string {
	var ns []string
	for _, c := range All {
		if slices.Contains(c.Lens, lens) {
			ns = append(ns, c.Name)
		}
	}
	return ns
}

// Pick returns the columns named, for lens, or the names that are not
// columns of it.
func Pick(lens Lens, names []string) ([]Column, []string) {
	var cs []Column
	var bad []string
	for _, n := range names {
		c, ok := Find(n)
		if !ok || !slices.Contains(c.Lens, lens) {
			bad = append(bad, n)
			continue
		}
		cs = append(cs, c)
	}
	return cs, bad
}

// Fit returns the columns that fit in width, dropping the ones with the
// highest Drop first, and the width each gets: a column of Width 0 takes
// what is left.
func Fit(cs []Column, width int) ([]Column, []int) {
	cs = slices.Clone(cs)
	for {
		used := 0
		for i, c := range cs {
			if i > 0 {
				used += Gap
			}
			w := c.Width
			if w == 0 {
				w = MinFlex
			}
			used += w
		}
		if used <= width {
			ws := make([]int, len(cs))
			for i, c := range cs {
				ws[i] = c.Width
				if c.Width == 0 {
					ws[i] = MinFlex + width - used
				}
			}
			return cs, ws
		}
		drop := -1
		for i, c := range cs {
			if c.Drop > 0 && (drop < 0 || c.Drop > cs[drop].Drop) {
				drop = i
			}
		}
		if drop < 0 {
			ws := make([]int, len(cs))
			for i, c := range cs {
				ws[i] = max(c.Width, MinFlex)
			}
			return cs, ws
		}
		cs = slices.Delete(cs, drop, drop+1)
	}
}

// Needs is what the columns need read from GitHub; a lens that shows
// repositories not cloned reads their fields too.
func Needs(lens Lens, cs []Column) model.Need {
	var n model.Need
	for _, c := range cs {
		n |= c.Need
	}
	if lens == GitHub && n != 0 {
		n |= model.NeedNotCloned
	}
	return n
}
