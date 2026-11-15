// Package config reads ~/.config/gh-tidy/config.toml. A setting the file
// leaves out takes its default, so a default changed later still applies.
// A mistake is reported where it is in the file rather than ignored.
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

	"github.com/babarot/gh-tidy/internal/theme"
)

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

// Core is where clones are and how they are read.
type Core struct {
	// Root is where clones live, laid out as <host>/<owner>/<repo>. Empty
	// reads git config ghq.root, then ~/ghq.
	Root string `toml:"root"`
}

// TUI is how the list looks.
type TUI struct {
	Theme string `toml:"theme"`
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
		Owners: Owners{Add: []string{}, Hide: []string{}},
		TUI:    TUI{Theme: theme.Auto},
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

// Load reads the file at path over the defaults; without a file, the
// defaults. Nothing is written: a file written once would keep the
// defaults of that version in its comments, and lack the settings added
// later. gh tidy --print-config prints the template.
func Load(path string) (File, error) {
	f := Default()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
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
	if err := removedSettings(data); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
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

// removed are the settings that are gone, by table and key, with why. A
// file that still sets one is told so, rather than told the key is
// unknown.
var removed = map[string]string{
	"core.default_owner":     "the owner opened is the one given, the one where gh tidy starts, the one opened last, or you",
	"core.fetch_concurrency": "how many fetches run at once is fixed, to spare GitHub and the machine",
	"core.stale_check":       "gh tidy always reads where GitHub's default branches are; MAIN is measured against them",
	"tui.status_style":       "[symbols] counts says whether counts are written",
	"tui.hide":               "the clones with nothing to do are a section at the end of the list, folded by its head",
	"tui.sort":               "the list is in the order of what needs attention",
	"tui.detail_height":      "the pane's height is changed with + and -, and kept for the next start",
}

func removedSettings(data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil // the strict decode says where
	}
	var errs []error
	for table, v := range raw {
		t, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for key := range t {
			if why, ok := removed[table+"."+key]; ok {
				errs = append(errs, fmt.Errorf("[%s] %s is no longer a setting: %s", table, key, why))
			}
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errors.Join(errs...)
}

func (f File) validate() error {
	errs := []error{checkSettings(f)}
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
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

// Template is every setting at its default, commented out, with what it
// does: what --print-config prints, and docs/configuration.md shows.
var Template = template()

// keysTemplate is [keys] in the template, every action at its default
// keys; a test keeps it the keymap's.
const keysTemplate = `[keys]
# Which keys do what, by action: a key or a list of keys, replacing the
# action's own, or [] to turn it off. ctrl+c and esc are fixed.
# quit = "q"
# help = "?"
# up = ["up", "k", "ctrl+p"]
# down = ["down", "j", "ctrl+n"]
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
# refresh = "r"
# list = "L"
# workers = "w"
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
`

// actionsTemplate is [[actions]] in the template: an example, not a
// default, so it stays commented.
const actionsTemplate = `# Custom commands, run on the selected repository or worktree. run is the
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
