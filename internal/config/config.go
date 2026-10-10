// Package config reads ~/.config/gh-fleet/config.toml. The file is written
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

	"github.com/pelletier/go-toml/v2"

	"github.com/babarot/gh-fleet/internal/theme"
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
	TUI     TUI            `toml:"tui"`
	Cleanup Cleanup        `toml:"cleanup"`
	Keys    map[string]any `toml:"keys"`
	Actions []Action       `toml:"actions"`
}

// Core is where clones are and how they are read.
type Core struct {
	// Root is where clones live, laid out as <host>/<owner>/<repo>. Empty
	// reads git config ghq.root, then ~/ghq.
	Root string `toml:"root"`
	// DefaultOwner is opened when gh fleet starts outside Root. Empty opens
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
		Core:    Core{FetchConcurrency: 8, StaleCheck: true},
		TUI:     TUI{Theme: theme.Auto, StatusStyle: StatusCount, Hide: []string{HideQuiet, HideUntracked}, Sort: SortAttention, DetailHeight: 8},
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
	return filepath.Join(dir, "gh-fleet", "config.toml")
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
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return f, fmt.Errorf("%s:%d:%d: %s\n%s", path, row, col, de.Error(), de.String())
		}
		var se *toml.StrictMissingError
		if errors.As(err, &se) {
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
const Template = `# gh fleet settings. Uncomment a line to change it.

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
# Which keys do what, by operation: a key or a list of keys, replacing the
# operation's own, or [] to turn it off. ctrl+c is fixed.
# up = ["up", "k"]
# down = ["down", "j"]
# top = ["home", "g"]
# bottom = ["end", "G"]
# page_up = ["pgup", "ctrl+b"]
# page_down = ["pgdown", "ctrl+f"]
# expand = "enter"
# switch_tab = "tab"
# search = "/"
# show_all = "a"
# sort = "s"
# fetch = "f"
# fetch_all = "F"
# pull = "p"
# pull_all = "P"
# cleanup = "d"
# clone = "c"
# open_browser = "o"
# reveal = "R"
# copy_path = "y"
# copy_url = "Y"
# help = "?"
# quit = ["q", "esc"]

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
