package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/theme"
)

// The settings of the tables are defined once, here: the template that
// --print-config prints and docs/configuration.md shows, and the check of
// each value, are made from this table. The file is decoded into File,
// and a test keeps the table and File's toml tags one to one. [keys] and
// [[actions]] are not in the table: an action's keys come from the
// keymap, and an action's fields are checked together.

// setting is one key of a table.
type setting struct {
	// Key is table.key.
	Key string
	// Doc is what the template says above the key.
	Doc  string
	Kind kind
	// Value is the setting in f.
	Value func(f File) any
}

// table is a table of the file, in the order the template writes them.
type table struct {
	Name string
	// Doc is what the template says under the table's header.
	Doc string
}

// kind is what values a setting takes.
type kind interface {
	// check says what is wrong with v, the setting's value, at key.
	check(key string, v any) []error
	// toml is v as the template writes it.
	toml(v any) string
}

// as is v, a setting's value, as the type its kind takes. The table pairs
// every kind with a value of that type, so another type is a mistake in
// the table and panics.
func as[T any](v any) T {
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("config: a setting's value is %T, not %T", v, t))
	}
	return t
}

// where is a setting's key as messages write it: [table] key.
func where(key string) string {
	table, name, _ := strings.Cut(key, ".")
	return "[" + table + "] " + name
}

type (
	boolKind   struct{}
	stringKind struct{}
	// themeKind is the name of a color scheme, or auto.
	themeKind struct{}
	// listKind is a list of strings, with at least one when NonEmpty.
	// Which names a list of columns or sections may hold is checked where
	// they are known.
	listKind struct{ NonEmpty bool }
	// ownersKind is a list of owners, as "owner" or "host/owner".
	ownersKind struct{}
	// markKind is a mark the list writes: printed, with no space, and one
	// cell wide per character, or the columns would not line up. Empty
	// writes nothing.
	markKind struct{}
	// commandKind is a command and its arguments, which must pass Needs.
	commandKind struct{ Needs string }
)

func (boolKind) check(string, any) []error { return nil }
func (boolKind) toml(v any) string         { return fmt.Sprint(as[bool](v)) }

func (stringKind) check(string, any) []error { return nil }
func (stringKind) toml(v any) string         { return fmt.Sprintf("%q", as[string](v)) }

func (themeKind) check(key string, v any) []error {
	if s := as[string](v); !theme.Valid(s) {
		return []error{fmt.Errorf("%s %q is not one of %s", where(key), s, theme.NamesString())}
	}
	return nil
}
func (themeKind) toml(v any) string { return fmt.Sprintf("%q", as[string](v)) }

func (k listKind) check(key string, v any) []error {
	if k.NonEmpty && len(as[[]string](v)) == 0 {
		return []error{fmt.Errorf("%s must name at least one", where(key))}
	}
	return nil
}
func (listKind) toml(v any) string { return tomlList(as[[]string](v)) }

func (ownersKind) check(key string, v any) []error {
	var errs []error
	for _, o := range as[[]string](v) {
		if n := strings.Count(o, "/"); o == "" || n > 1 || strings.HasPrefix(o, "/") || strings.HasSuffix(o, "/") {
			errs = append(errs, fmt.Errorf("%s: %q is not \"owner\" or \"host/owner\"", where(key), o))
		}
	}
	return errs
}
func (ownersKind) toml(v any) string { return tomlList(as[[]string](v)) }

func (markKind) check(key string, v any) []error {
	s := as[string](v)
	switch {
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }):
		return []error{fmt.Errorf("%s %q has a space or a character that is not printed", where(key), s)}
	case ansi.StringWidth(s) != len([]rune(s)):
		return []error{fmt.Errorf("%s %q has a character two cells wide, which would put the columns out of line", where(key), s)}
	}
	return nil
}
func (markKind) toml(v any) string { return fmt.Sprintf("%q", as[string](v)) }

func (k commandKind) check(key string, v any) []error {
	argv := as[[]string](v)
	switch {
	case len(argv) == 0:
		return []error{fmt.Errorf("%s must name a command", where(key))}
	case k.Needs != "" && !slices.Contains(argv, k.Needs):
		return []error{fmt.Errorf("%s must pass %s", where(key), k.Needs)}
	}
	return nil
}
func (commandKind) toml(v any) string { return tomlList(as[[]string](v)) }

func tomlList(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// tables are the tables of the file, in the order the template writes
// them.
var tables = []table{
	{Name: "core"},
	{Name: "owners", Doc: "The owners offered are the viewer, the viewer's organizations and the owners with clones under root."},
	{Name: "tui"},
	{Name: "columns", Doc: "The columns of each set, in order. v switches between the sets."},
	{Name: "preview"},
	{Name: "symbols", Doc: "The marks the list writes, after zsh-mini-prompt. Change them to match your prompt; each must be one cell wide per character, and an empty one writes nothing."},
	{Name: "cleanup"},
}

// settings are every key of the tables, table by table in their order.
var settings = []setting{
	{Key: "core.root", Kind: stringKind{}, Value: func(f File) any { return f.Core.Root },
		Doc: "Where clones live, laid out as <host>/<owner>/<repo>. Empty (default) reads git config ghq.root, then ~/ghq."},

	{Key: "owners.add", Kind: ownersKind{}, Value: func(f File) any { return f.Owners.Add },
		Doc: `Offer these owners too, as "owner" or "host/owner".`},
	{Key: "owners.hide", Kind: ownersKind{}, Value: func(f File) any { return f.Owners.Hide },
		Doc: "Leave these owners out, written the same way."},

	{Key: "tui.theme", Kind: themeKind{}, Value: func(f File) any { return f.TUI.Theme },
		Doc: `Color scheme: "auto" (default) picks catppuccin-mocha on a dark terminal and catppuccin-latte on a light one. Also: tokyo-night, dracula, nord, gruvbox-dark, and ansi (the terminal's own 16 colors).`},

	{Key: "columns.local", Kind: listKind{NonEmpty: true}, Value: func(f File) any { return f.Columns.Local },
		Doc: "The local set, of: repo, main, checked_out, changes, worktrees, fetched."},
	{Key: "columns.github", Kind: listKind{NonEmpty: true}, Value: func(f File) any { return f.Columns.GitHub },
		Doc: "The github set, of: repo, issues, prs, dependabot, ci, stars, pushed, visibility, main."},

	{Key: "preview.sections", Kind: listKind{}, Value: func(f File) any { return f.Preview.Sections },
		Doc: "The parts of the pane below the list, in order: incoming (commits on origin's default branch that p brings in), unpushed, behind_main (commits on the default branch the checked out branch lacks), changes (the files changed), stash and worktrees. A part with nothing to show is left out."},

	{Key: "symbols.unstaged", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Unstaged }, Doc: "Files changed, not staged."},
	{Key: "symbols.staged", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Staged }, Doc: "Files staged."},
	{Key: "symbols.untracked", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Untracked }, Doc: "Files not tracked."},
	{Key: "symbols.stash", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Stash }, Doc: "Stash entries."},
	{Key: "symbols.behind", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Behind }, Doc: "Behind: commits to bring in."},
	{Key: "symbols.ahead", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Ahead }, Doc: "Ahead: commits not pushed."},
	{Key: "symbols.diverged", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Diverged }, Doc: "Behind and ahead, when counts are not written."},
	{Key: "symbols.synced", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Synced }, Doc: "Even with what it follows."},
	{Key: "symbols.behind_main", Kind: markKind{}, Value: func(f File) any { return f.Symbols.BehindMain }, Doc: "A branch behind the default branch."},
	{Key: "symbols.github_ahead", Kind: markKind{}, Value: func(f File) any { return f.Symbols.GitHubAhead }, Doc: "GitHub has commits not fetched yet."},
	{Key: "symbols.worktree", Kind: markKind{}, Value: func(f File) any { return f.Symbols.Worktree }, Doc: "Worktrees besides the main one."},
	{Key: "symbols.counts", Kind: boolKind{}, Value: func(f File) any { return f.Symbols.Counts },
		Doc: "Write how many after a mark (*3); false writes the mark alone (*)."},

	{Key: "cleanup.protect", Kind: listKind{}, Value: func(f File) any { return f.Cleanup.Protect },
		Doc: "Branches d never deletes, besides the default branch and the current one."},
	{Key: "cleanup.delete_gone", Kind: boolKind{}, Value: func(f File) any { return f.Cleanup.DeleteGone },
		Doc: "Also delete branches whose upstream is gone but which are not merged (a squash merge), with git branch -D."},
	{Key: "cleanup.worktree_remove", Kind: commandKind{Needs: "{path}"}, Value: func(f File) any { return f.Cleanup.WorktreeRemove },
		Doc: "How d removes a worktree it decided to remove, as a command and its arguments, run in the repository. It must pass {path}. For worktrees made by another tool, its own command can do it, so the tool forgets it too."},
}

// checkSettings says what is wrong with the settings of the tables.
func checkSettings(f File) error {
	var errs []error
	for _, s := range settings {
		errs = append(errs, s.Kind.check(s.Key, s.Value(f))...)
	}
	return errors.Join(errs...)
}

// template writes the tables from the table, every setting at its
// default, commented out, then [keys] and [[actions]].
func template() string {
	var b strings.Builder
	b.WriteString("# gh tidy settings. Uncomment a line to change it.\n")
	def := Default()
	for _, t := range tables {
		b.WriteString("\n[" + t.Name + "]\n")
		b.WriteString(comment(t.Doc))
		for _, s := range settings {
			if table, key, _ := strings.Cut(s.Key, "."); table == t.Name {
				b.WriteString(comment(s.Doc) + "# " + key + " = " + s.Kind.toml(s.Value(def)) + "\n")
			}
		}
	}
	return b.String() + "\n" + keysTemplate + "\n" + actionsTemplate
}

// comment is text as comment lines, wrapped at 78 columns.
func comment(text string) string {
	if text == "" {
		return ""
	}
	var b strings.Builder
	line := "#"
	for w := range strings.FieldsSeq(text) {
		if len(line)+1+len(w) > 78 && line != "#" {
			b.WriteString(line + "\n")
			line = "#"
		}
		line += " " + w
	}
	return b.String() + line + "\n"
}
