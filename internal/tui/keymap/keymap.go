// Package keymap binds keys to actions by name. The TUI never writes a key
// in its code: it asks for an action ("fetch", "lens") and draws the key
// hints and the ? list from here, so every key can be changed under [keys]
// in the config file.
//
// Keys are written as Bubble Tea reports key presses: "a", "A", "enter",
// "ctrl+f", "shift+tab".
package keymap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Action is something a key does, named as it is under [keys].
type Action string

// Scope is where an action's keys are read. An action in ScopeGlobal or
// ScopeNav is read in every other scope too, so its keys may not be used by
// any other action; an action in another scope only shares its scope.
type Scope string

const (
	ScopeGlobal  Scope = "global"
	ScopeNav     Scope = "nav"
	ScopeList    Scope = "list"
	ScopePopup   Scope = "popup"
	ScopeConfirm Scope = "confirm"
	// ScopeInput is read while a text box has the keys, which types every
	// other key it is given.
	ScopeInput Scope = "input"
)

// Def is an action with its default keys, the words the key hints show,
// and what it does, for the list of keys.
type Def struct {
	Action Action
	Scope  Scope
	Keys   []string
	Help   string
	Doc    string
}

// Defaults are every action with its keys, in the order the ? list shows
// them.
var Defaults = []Def{
	{"quit", ScopeGlobal, []string{"q"}, "quit", "quits, once the changes running end"},
	{"help", ScopeGlobal, []string{"?"}, "keys", "shows every key"},

	// ctrl+p and ctrl+n move as the arrows and k and j do, also in a
	// popup's filter, which types k and j.
	{"up", ScopeNav, []string{"up", "k", "ctrl+p"}, "up", "moves the cursor up"},
	{"down", ScopeNav, []string{"down", "j", "ctrl+n"}, "down", "moves the cursor down"},
	{"top", ScopeNav, []string{"home", "g"}, "top", "moves to the first row"},
	{"bottom", ScopeNav, []string{"end", "G"}, "bottom", "moves to the last row"},
	{"page_up", ScopeNav, []string{"pgup", "ctrl+b"}, "page up", "moves a screen up"},
	{"page_down", ScopeNav, []string{"pgdown", "ctrl+f"}, "page down", "moves a screen down"},

	{"expand", ScopeList, []string{"enter"}, "worktrees", "shows a clone's worktrees, or folds a section"},
	{"lens", ScopeList, []string{"v"}, "local / github", "switches between the local and the github columns"},
	{"prev_owner", ScopeList, []string{"<"}, "previous owner", "shows the previous owner"},
	{"next_owner", ScopeList, []string{">"}, "next owner", "shows the next owner"},
	{"choose_owner", ScopeList, []string{"O"}, "owner", "chooses an owner from a list"},
	{"search", ScopeList, []string{"/"}, "filter", "filters the rows by name or branch"},
	{"fetch", ScopeList, []string{"f"}, "fetch", "fetches the clone"},
	{"fetch_all", ScopeList, []string{"F"}, "fetch all", "fetches every clone"},
	// The default branch, not the branch checked out.
	{"pull", ScopeList, []string{"p"}, "pull", "moves the clone's default branch forward"},
	{"pull_all", ScopeList, []string{"P"}, "pull all", "fetches every clone, then moves each default branch forward; asks first"},
	{"cleanup", ScopeList, []string{"d"}, "clean up", "cleans up the clone; asks first"},
	{"cleanup_all", ScopeList, []string{"D"}, "clean up all", "cleans up every clone's finished worktrees; asks first"},
	{"clone", ScopeList, []string{"c"}, "clone", "clones the repository selected"},
	// Reads the clones and GitHub again, all of them, fresh or not.
	{"refresh", ScopeList, []string{"r"}, "read again", "reads the clones and GitHub again, all of them"},
	// Reads the list of an owner gh tidy does not read on its own.
	{"list", ScopeList, []string{"L"}, "read the list", "reads the list of an owner that is not yours"},
	// Shows how many of each kind of work run, under the activity box.
	{"workers", ScopeList, []string{"w"}, "workers", "shows how many of each kind of work run, while gh tidy works"},
	{"open_browser", ScopeList, []string{"o"}, "open on GitHub", "opens the repository on GitHub"},
	{"reveal", ScopeList, []string{"R"}, "reveal in Finder", "shows the clone in Finder"},
	{"copy_path", ScopeList, []string{"y"}, "copy path", "copies the path"},
	{"copy_url", ScopeList, []string{"Y"}, "copy url", "copies the URL"},
	{"preview_grow", ScopeList, []string{"+"}, "taller pane", "makes the pane taller"},
	{"preview_shrink", ScopeList, []string{"-"}, "shorter pane", "makes the pane shorter"},
	{"preview_down", ScopeList, []string{"J"}, "scroll pane down", "scrolls the pane down"},
	{"preview_up", ScopeList, []string{"K"}, "scroll pane up", "scrolls the pane up"},

	{"choose", ScopePopup, []string{"enter"}, "choose", "chooses the item"},

	// A confirm popup reads no choose, so enter answers it too.
	{"confirm", ScopeConfirm, []string{"y", "enter"}, "yes", "runs the change asked about"},

	{"accept", ScopeInput, []string{"enter"}, "keep", "keeps the filter"},
	{"erase", ScopeInput, []string{"backspace"}, "erase", "erases a character"},
}

// removed are the actions that are gone, with nothing in their place, by
// name, with why. A [keys] entry for one is a mistake that says so.
var removed = map[string]string{
	"switch_tab": "the repositories not cloned are a section at the end of the list, folded by its head",
	"show_all":   "the clones with nothing to do are a section at the end of the list, folded by its head",
	"sort":       "the list is in the order of what needs attention",
}

// The fixed keys are read before any action and cannot be bound.
const (
	// Interrupt quits from anywhere.
	Interrupt = "ctrl+c"
	// Back steps back one level everywhere; at the top it only says how to quit.
	Back = "esc"
)

// Fixed are the fixed keys and what each does.
var Fixed = map[string]string{
	Interrupt: "quits from anywhere",
	Back:      "steps back one level everywhere",
}

// Known reports whether name is an action, so that the TUI being
// replaced can leave the new actions in [keys] to the new one.
func Known(name string) bool {
	return slices.ContainsFunc(Defaults, func(d Def) bool { return string(d.Action) == name })
}

// Map is the keys of every action, after the config file.
type Map struct {
	defs map[Action]Def
	keys map[Action][]string
}

// Default is the map with every action at its default keys.
func Default() Map {
	m := Map{defs: map[Action]Def{}, keys: map[Action][]string{}}
	for _, d := range Defaults {
		m.defs[d.Action] = d
		m.keys[d.Action] = slices.Clone(d.Keys)
	}
	return m
}

// Keys are action's keys; none for an action that does not exist or was
// turned off.
func (m Map) Keys(a Action) []string { return slices.Clone(m.keys[a]) }

// First is action's first key, for a hint; empty when it has none.
func (m Map) First(a Action) string {
	if ks := m.keys[a]; len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// Help is the words the key hints show for action.
func (m Map) Help(a Action) string { return m.defs[a].Help }

// Doc says what action does, for the list of keys.
func (m Map) Doc(a Action) string { return m.defs[a].Doc }

// Matches reports whether a key press, as Bubble Tea writes it, is one of
// action's keys.
func (m Map) Matches(a Action, pressed string) bool {
	return slices.Contains(m.keys[a], pressed)
}

// Actions are the actions of scope, in the order of Defaults.
func (m Map) Actions(s Scope) []Action {
	var as []Action
	for _, d := range Defaults {
		if d.Scope == s {
			as = append(as, d.Action)
		}
	}
	return as
}

// Apply returns base with the actions in set given those keys, replacing
// their own; a key or a list of keys, and [] turns an action off. A key
// set for an action is taken away from any action that has it by default
// and would meet it, so fetch = "x" does not leave x also on its default
// owner. Every mistake is returned rather than ignored, since a key that
// never matches looks like a bug: an unknown action, a key no key press is
// read as, a fixed key, and two actions set to the same key where they
// meet.
func Apply(base Map, set map[string]any) (Map, error) {
	m := Map{defs: base.defs, keys: map[Action][]string{}}
	for a, ks := range base.keys {
		m.keys[a] = slices.Clone(ks)
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	slices.Sort(names)
	var errs []error
	given := map[Action]bool{}
	for _, n := range names {
		a := Action(n)
		if why, ok := removed[n]; ok {
			errs = append(errs, fmt.Errorf("[keys] %s is no longer an action: %s", n, why))
			continue
		}
		if _, ok := m.defs[a]; !ok {
			errs = append(errs, fmt.Errorf("[keys] %s", unknownAction(n)))
			continue
		}
		keys, err := keyList(set[n])
		if err != nil {
			errs = append(errs, fmt.Errorf("[keys] %s %w", n, err))
			continue
		}
		bad := false
		for _, k := range keys {
			if err := CheckKey(k); err != nil {
				errs = append(errs, fmt.Errorf("[keys] %s: %w", n, err))
				bad = true
			}
		}
		if !bad {
			m.keys[a] = keys
			given[a] = true
		}
	}
	if len(errs) > 0 {
		return Map{}, errors.Join(errs...)
	}
	// A key set in the file wins over a default it meets.
	for _, c := range m.conflicts() {
		if given[c.first] == given[c.second] {
			continue
		}
		loser := c.second
		if given[c.second] {
			loser = c.first
		}
		m.keys[loser] = slices.DeleteFunc(m.keys[loser], func(k string) bool { return k == c.key })
	}
	for _, c := range m.conflicts() {
		errs = append(errs, fmt.Errorf("[keys] %s: %q is also %s's key", c.second, c.key, c.first))
	}
	if len(errs) > 0 {
		return Map{}, errors.Join(errs...)
	}
	return m, nil
}

// keyList takes a key or a list of keys, as the config file decodes them.
func keyList(v any) ([]string, error) {
	switch v := v.(type) {
	case string:
		return []string{v}, nil
	case []string:
		return slices.Clone(v), nil
	case []any:
		keys := []string{}
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, errors.New("must be a key or a list of keys, as strings")
			}
			keys = append(keys, s)
		}
		return keys, nil
	}
	return nil, errors.New("must be a key or a list of keys, as strings")
}

type conflict struct {
	key           string
	first, second Action
}

// meets reports whether keys of actions in scopes a and b can be read at the
// same time.
func meets(a, b Scope) bool {
	wide := func(s Scope) bool { return s == ScopeGlobal || s == ScopeNav }
	return a == b || wide(a) || wide(b)
}

// conflicts are the keys two actions share where they meet, in the order of
// Defaults.
func (m Map) conflicts() []conflict {
	var cs []conflict
	for i, a := range Defaults {
		for _, b := range Defaults[i+1:] {
			if !meets(a.Scope, b.Scope) {
				continue
			}
			for _, k := range m.keys[a.Action] {
				if slices.Contains(m.keys[b.Action], k) {
					cs = append(cs, conflict{key: k, first: a.Action, second: b.Action})
				}
			}
		}
	}
	return cs
}

// unknownAction says an action does not exist, naming a close one when
// there is.
func unknownAction(name string) string {
	for _, d := range Defaults {
		if a := string(d.Action); strings.Contains(a, name) || strings.Contains(name, a) {
			return fmt.Sprintf("%q is not an action; did you mean %q?", name, a)
		}
	}
	return fmt.Sprintf("%q is not an action", name)
}
