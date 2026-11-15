// Package config reads ~/.config/gh-tidy/config.toml. The file is written
// on first run with every setting commented out at its default, so a default
// changed later still applies until the user picks a value. A mistake is
// reported where it is in the file rather than ignored.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/pelletier/go-toml/v2"

	"github.com/babarot/gh-tidy/internal/theme"
)

// How STATUS is written.
const (
	StatusCount  = "count"  // 3* 1+ 12% 2$
	StatusSymbol = "symbol" // *+%$, as zsh-mini-prompt writes it
)

// What [tui] hide can leave out until a shows everything.
const (
	HideQuiet     = "quiet"     // nothing to say
	HideUntracked = "untracked" // only untracked files to say
)

// Sort orders of the list.
const (
	SortAttention  = "attention"
	SortName       = "name"
	SortFetched    = "fetched"
	SortLastCommit = "last_commit"
)

// Sorts lists the sort orders in the order s cycles through them.
var Sorts = []string{SortAttention, SortName, SortFetched, SortLastCommit}

// Scopes of a custom action.
const (
	ScopeRepo     = "repo"     // the selected repository
	ScopeWorktree = "worktree" // the selected worktree, or the repository's main one
)

// Placeholders lists what a custom action's run and worktree_remove may use.
var Placeholders = []string{"{path}", "{repo}", "{owner}", "{name}", "{branch}", "{default}", "{url}"}

// File is the config file.
type File struct {
	Core    Core           `toml:"core"`
	Owners  Owners         `toml:"owners"`
	TUI     TUI            `toml:"tui"`
	Columns Columns        `toml:"columns"`
	Preview Preview        `toml:"preview"`
	Symbols Symbols        `toml:"symbols"`
	Cleanup Cleanup        `toml:"cleanup"`
	Keys    map[string]any `toml:"keys"`
	Actions []Action       `toml:"actions"`
}

// Owners changes the owners offered: the viewer, the viewer's
// organizations and the owners with clones under the root.
type Owners struct {
	// Add offers these owners too, as "owner" or "host/owner".
	Add []string `toml:"add"`
	// Hide leaves these owners out, written the same way.
	Hide []string `toml:"hide"`
}

// Columns are the columns of each set, by name, in order. The names are
// checked against the columns the TUI has.
type Columns struct {
	Local  []string `toml:"local"`
	GitHub []string `toml:"github"`
}

// Preview is what the pane below the list shows of the selected row.
type Preview struct {
	// Sections are the parts of the pane, by name, in order.
	Sections []string `toml:"sections"`
}

// Symbols are the marks the list writes, after zsh-mini-prompt by
// default, so that they read as the user's prompt does. Each must be as
// many cells wide as it has characters, or the columns would not line up;
// an empty one writes nothing (starship writes nothing for up to date).
type Symbols struct {
	Unstaged    string `toml:"unstaged"`
	Staged      string `toml:"staged"`
	Untracked   string `toml:"untracked"`
	Stash       string `toml:"stash"`
	Behind      string `toml:"behind"`
	Ahead       string `toml:"ahead"`
	Diverged    string `toml:"diverged"`
	Synced      string `toml:"synced"`
	BehindMain  string `toml:"behind_main"`
	GitHubAhead string `toml:"github_ahead"`
	Worktree    string `toml:"worktree"`
	// Counts writes how many after a mark (*3), or the mark alone (*).
	Counts bool `toml:"counts"`
}

// Each returns the marks with their names in the file, for checking them.
func (s Symbols) Each() [][2]string {
	return [][2]string{{"unstaged", s.Unstaged}, {"staged", s.Staged}, {"untracked", s.Untracked}, {"stash", s.Stash},
		{"behind", s.Behind}, {"ahead", s.Ahead}, {"diverged", s.Diverged}, {"synced", s.Synced},
		{"behind_main", s.BehindMain}, {"github_ahead", s.GitHubAhead}, {"worktree", s.Worktree}}
}

// Core is where clones are and how they are read.
type Core struct {
	// Root is where clones live, laid out as <host>/<owner>/<repo>. Empty
	// reads git config ghq.root, then ~/ghq.
	Root string `toml:"root"`
	// DefaultOwner is opened when gh tidy starts outside Root. Empty opens
	// the owner picker.
	DefaultOwner string `toml:"default_owner"`
	// FetchConcurrency is how many repositories F fetches at once.
	FetchConcurrency int `toml:"fetch_concurrency"`
	// StaleCheck asks GitHub whether each default branch has moved past the
	// local origin/<default>.
	StaleCheck bool `toml:"stale_check"`
}

// TUI is how the list looks.
type TUI struct {
	Theme        string   `toml:"theme"`
	StatusStyle  string   `toml:"status_style"`
	Hide         []string `toml:"hide"`
	Sort         string   `toml:"sort"`
	DetailHeight int      `toml:"detail_height"`
}

// Cleanup is what d removes and how.
type Cleanup struct {
	// Protect lists branches d never deletes, besides the default branch
	// and the current one.
	Protect []string `toml:"protect"`
	// DeleteGone also deletes branches whose upstream is gone but which are
	// not merged (a squash merge), with git branch -D.
	DeleteGone bool `toml:"delete_gone"`
	// WorktreeRemove is the command that removes a worktree d decided to
	// remove, as argv with placeholders.
	WorktreeRemove []string `toml:"worktree_remove"`
}

// Action is a custom command run on the selected repository or worktree.
type Action struct {
	Key  string `toml:"key"`
	Name string `toml:"name"`
	// Scope is ScopeRepo or ScopeWorktree.
	Scope string   `toml:"scope"`
	Run   []string `toml:"run"`
	// Interactive hands the terminal to the command, for one that asks or
	// draws; otherwise its output is captured and its exit shown.
	Interactive bool `toml:"interactive"`
	// Confirm asks before running it, for a command that removes or
	// changes something.
	Confirm bool `toml:"confirm"`
}

// Default returns the settings used when the file sets nothing.
func Default() File {
	return File{
		Core:   Core{FetchConcurrency: 8, StaleCheck: true},
		Owners: Owners{Add: []string{}, Hide: []string{}},
		TUI:    TUI{Theme: theme.Auto, StatusStyle: StatusCount, Hide: []string{HideQuiet, HideUntracked}, Sort: SortAttention, DetailHeight: 8},
		Columns: Columns{
			Local:  []string{"repo", "main", "checked_out", "changes", "worktrees", "fetched"},
			GitHub: []string{"repo", "issues", "prs", "dependabot", "ci", "pushed", "visibility", "main"},
		},
		Preview: Preview{Sections: []string{"incoming", "unpushed", "behind_main", "changes", "stash", "worktrees"}},
		Symbols: Symbols{Unstaged: "*", Staged: "+", Untracked: "%", Stash: "$", Behind: "<", Ahead: ">", Diverged: "<>",
			Synced: "=", BehindMain: "⇣", GitHubAhead: "?", Worktree: "⌥", Counts: true},
		Cleanup: Cleanup{Protect: []string{}, WorktreeRemove: []string{"git", "worktree", "remove", "{path}"}},
	}
}

// Path is the config file, honoring XDG_CONFIG_HOME.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		h, _ := os.UserHomeDir()
		dir = filepath.Join(h, ".config")
	}
	return filepath.Join(dir, "gh-tidy", "config.toml")
}

// Load reads the file at path over the defaults, writing the template there
// first when there is no file.
func Load(path string) (File, error) {
	f := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, []byte(Template), 0o644)
		}
		return f, nil
	}
	if err != nil {
		return f, err
	}
	return Parse(path, data)
}

// Parse decodes data, named path in messages, over the defaults.
func Parse(path string, data []byte) (File, error) {
	f := Default()
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		if de, ok := errors.AsType[*toml.DecodeError](err); ok {
			row, col := de.Position()
			return f, fmt.Errorf("%s:%d:%d: %s\n%s", path, row, col, de.Error(), de.String())
		}
		if se, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			return f, fmt.Errorf("%s: %s", path, strings.TrimSpace(se.String()))
		}
		return f, fmt.Errorf("%s: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (f File) validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !theme.Valid(f.TUI.Theme) {
		bad("[tui] theme %q is not one of %s", f.TUI.Theme, theme.NamesString())
	}
	if f.TUI.StatusStyle != StatusCount && f.TUI.StatusStyle != StatusSymbol {
		bad("[tui] status_style %q is not %q or %q", f.TUI.StatusStyle, StatusCount, StatusSymbol)
	}
	for _, h := range f.TUI.Hide {
		if h != HideQuiet && h != HideUntracked {
			bad("[tui] hide %q is not %q or %q", h, HideQuiet, HideUntracked)
		}
	}
	if !slices.Contains(Sorts, f.TUI.Sort) {
		bad("[tui] sort %q is not one of %s", f.TUI.Sort, strings.Join(Sorts, ", "))
	}
	if f.TUI.DetailHeight < 5 {
		bad("[tui] detail_height must be at least 5, not %d", f.TUI.DetailHeight)
	}
	if f.Core.FetchConcurrency < 1 {
		bad("[core] fetch_concurrency must be at least 1, not %d", f.Core.FetchConcurrency)
	}
	for _, o := range slices.Concat(f.Owners.Add, f.Owners.Hide) {
		if n := strings.Count(o, "/"); o == "" || n > 1 || strings.HasPrefix(o, "/") || strings.HasSuffix(o, "/") {
			bad("[owners] %q is not \"owner\" or \"host/owner\"", o)
		}
	}
	if len(f.Columns.Local) == 0 || len(f.Columns.GitHub) == 0 {
		bad("[columns] local and github must each name a column")
	}
	for _, kv := range f.Symbols.Each() {
		name, v := kv[0], kv[1]
		switch {
		case strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }):
			bad("[symbols] %s %q has a space or a character that is not printed", name, v)
		case ansi.StringWidth(v) != len([]rune(v)):
			bad("[symbols] %s %q has a character two cells wide, which would put the columns out of line", name, v)
		}
	}
	if len(f.Cleanup.WorktreeRemove) == 0 {
		bad("[cleanup] worktree_remove must name a command")
	} else if !slices.Contains(f.Cleanup.WorktreeRemove, "{path}") {
		bad("[cleanup] worktree_remove must pass {path}")
	}
	for i, a := range f.Actions {
		where := fmt.Sprintf("[[actions]] #%d", i+1)
		if a.Name != "" {
			where += fmt.Sprintf(" (%s)", a.Name)
		}
		if a.Key == "" {
			bad("%s: key is missing", where)
		}
		if len(a.Run) == 0 {
			bad("%s: run must name a command", where)
		}
		if a.Scope != "" && a.Scope != ScopeRepo && a.Scope != ScopeWorktree {
			bad("%s: scope %q is not %q or %q", where, a.Scope, ScopeRepo, ScopeWorktree)
		}
	}
	return errors.Join(errs...)
}

// Hides reports whether [tui] hide lists h.
func (f File) Hides(h string) bool { return slices.Contains(f.TUI.Hide, h) }

// Expand replaces the placeholders in argv with vars, such as
// {"path": "/x"} for {path}. Each element stays one argument, so a path
// with spaces is passed whole.
func Expand(argv []string, vars map[string]string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		for k, v := range vars {
			a = strings.ReplaceAll(a, "{"+k+"}", v)
		}
		out[i] = a
	}
	return out
}

// Template is the file written on first run.
const Template = `# gh tidy settings. Uncomment a line to change it.

[core]
# Where clones live, laid out as <host>/<owner>/<repo>. Empty (default) reads
# git config ghq.root, then ~/ghq.
# root = ""
# The owner to open when started outside root. Empty (default) opens the
# owner picker.
# default_owner = ""
# How many repositories to fetch at once with F.
# fetch_concurrency = 8
# Ask GitHub (one GraphQL query) whether each default branch has moved past
# the local origin/<default>, and mark the counts that are out of date with ?.
# It also lists the repositories not cloned yet.
# stale_check = true

[owners]
# The owners offered are the viewer, the viewer's organizations and the
# owners with clones under root. Offer these too, as "owner" or "host/owner".
# add = []
# Leave these out, written the same way.
# hide = []

[tui]
# Color scheme: "auto" (default) picks catppuccin-mocha on a dark terminal and
# catppuccin-latte on a light one. Also: tokyo-night, dracula, nord,
# gruvbox-dark, and ansi (the terminal's own 16 colors).
# theme = "auto"
# How STATUS is written: "count" (default), such as 3* 1+ 12% 2$, or
# "symbol", such as *+%$, as zsh-mini-prompt writes it.
# status_style = "count"
# Repositories left out until a shows them: "quiet", nothing to say, and
# "untracked", only untracked files to say. [] shows every repository.
# hide = ["quiet", "untracked"]
# The order to start with: "attention" (default), "name", "fetched" or
# "last_commit". s cycles through them.
# sort = "attention"
# Height of the detail pane below the list, in lines (at least 5).
# detail_height = 8

[columns]
# The columns of each set, in order. v switches between the sets.
# local: repo, main, checked_out, changes, worktrees, fetched.
# github: repo, issues, prs, dependabot, ci, pushed, visibility, main.
# local = ["repo", "main", "checked_out", "changes", "worktrees", "fetched"]
# github = ["repo", "issues", "prs", "dependabot", "ci", "pushed", "visibility", "main"]

[preview]
# The parts of the pane below the list, in order: incoming (commits on
# origin's default branch that p brings in), unpushed, behind_main (commits
# on the default branch the checked out branch lacks), changes (the files
# changed), stash and worktrees. A part with nothing to show is left out.
# sections = ["incoming", "unpushed", "behind_main", "changes", "stash", "worktrees"]

[symbols]
# The marks the list writes, after zsh-mini-prompt. Change them to match
# your prompt; each must be one cell wide per character.
# unstaged = "*"
# staged = "+"
# untracked = "%"
# stash = "$"
# behind = "<"
# ahead = ">"
# diverged = "<>"
# synced = "="
# behind_main = "⇣"
# github_ahead = "?"
# worktree = "⌥"
# Write how many after a mark (*3); false writes the mark alone (*).
# counts = true

[cleanup]
# Branches d never deletes, besides the default branch and the current one.
# protect = []
# Also delete branches whose upstream is gone but which are not merged (a
# squash merge), with git branch -D.
# delete_gone = false
# How d removes a worktree it decided to remove, as a command and its
# arguments, run in the repository. It must pass {path}. For worktrees made
# by another tool, its own command can do it, so the tool forgets it too.
# worktree_remove = ["git", "worktree", "remove", "{path}"]

[keys]
# Which keys do what, by action: a key or a list of keys, replacing the
# action's own, or [] to turn it off. ctrl+c and esc are fixed.
# quit = "q"
# help = "?"
# up = ["up", "k"]
# down = ["down", "j"]
# top = ["home", "g"]
# bottom = ["end", "G"]
# page_up = ["pgup", "ctrl+b"]
# page_down = ["pgdown", "ctrl+f"]
# expand = "enter"
# lens = "v"
# prev_owner = "<"
# next_owner = ">"
# choose_owner = "O"
# search = "/"
# fetch = "f"
# fetch_all = "F"
# pull = "p"
# pull_all = "P"
# cleanup = "d"
# cleanup_all = "D"
# clone = "c"
# list = "L"
# open_browser = "o"
# reveal = "R"
# copy_path = "y"
# copy_url = "Y"
# preview_grow = "+"
# preview_shrink = "-"
# preview_down = "J"
# preview_up = "K"
# choose = "enter"
# confirm = ["y", "enter"]
# accept = "enter"
# erase = "backspace"

# Custom commands, run on the selected repository or worktree. run is the
# command and its arguments; these are replaced in each argument:
# {path}, {repo} (owner/name), {owner}, {name}, {branch}, {default}, {url}.
#
# [[actions]]
# key = "H"
# name = "open in herdr"
# scope = "worktree"   # "repo" (default) or "worktree"
# run = ["herdr", "worktree", "open", "--path", "{path}"]
# interactive = false  # true hands the terminal to the command
# confirm = false      # true asks before running it
`
