package tui

import (
	"fmt"
	"strings"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/tui/keymap"
)

// helpSection is a part of the list of keys: what keys, buttons or marks
// do where. The ? popup and docs/keys.md are drawn from the same sections,
// so that the page says what the screen does.
type helpSection struct {
	title string
	rows  []helpRow
	// words says the rows name what is done with the mouse, not keys.
	words bool
}

// helpRow is a key or a mouse button, or a mark; action is its name
// under [keys], empty for what cannot be changed there.
type helpRow struct {
	keys   []string
	action keymap.Action
	does   string
}

// helpScopes name the scopes of the keymap, in the order the list of keys
// shows them.
var helpScopes = []struct {
	scope keymap.Scope
	title string
}{
	{keymap.ScopeGlobal, "Anywhere"},
	{keymap.ScopeNav, "Moving, in the list and in a popup"},
	{keymap.ScopeList, "The list"},
	{keymap.ScopePopup, "A popup with a list"},
	{keymap.ScopeConfirm, "A popup that asks before a change"},
	{keymap.ScopeInput, "Typing the filter"},
}

// helpSections are every key, with the keys the user set, the actions the
// config file adds, what the mouse does, and the marks.
func helpSections(keys keymap.Map, actions []config.Action, sym config.Marks) []helpSection {
	var out []helpSection
	for _, s := range helpScopes {
		sec := helpSection{title: s.title}
		for _, a := range keys.Actions(s.scope) {
			if ks := keys.Keys(a); len(ks) > 0 {
				sec.rows = append(sec.rows, helpRow{keys: ks, action: a, does: keys.Doc(a)})
			}
		}
		switch s.scope {
		case keymap.ScopeGlobal:
			sec.rows = append(sec.rows, helpRow{keys: []string{keymap.Back}, does: keymap.Fixed[keymap.Back]},
				helpRow{keys: []string{keymap.Interrupt}, does: keymap.Fixed[keymap.Interrupt]})
		case keymap.ScopePopup:
			sec.rows = append(sec.rows, helpRow{keys: []string{"type"}, does: "narrows the list"})
		}
		out = append(out, sec)
	}
	if len(actions) > 0 {
		sec := helpSection{title: "Actions, from [[actions]]"}
		for _, a := range actions {
			does := fmt.Sprintf("%s · runs on the %s", a.Name, a.Scope)
			if a.Confirm {
				does += ", asking first"
			}
			sec.rows = append(sec.rows, helpRow{keys: []string{a.Key}, does: does})
		}
		out = append(out, sec)
	}
	out = append(out, helpSection{title: "The mouse, which only moves, opens and folds", words: true, rows: []helpRow{
		{keys: []string{"click"}, does: "puts the cursor on a row; on a section's head, folds or unfolds it"},
		{keys: []string{"wheel"}, does: "over the list moves the cursor; over the pane scrolls it"},
		{keys: []string{"click the owner"}, does: "chooses an owner; ‹ and › step to the previous and next"},
		{keys: []string{"click local, github"}, does: "switches the columns"},
		{keys: []string{"click a pane's head"}, does: "folds that part of the pane"},
		{keys: []string{"drag the pane's top"}, does: "makes the pane taller or shorter, as + and - do"},
		{keys: []string{"click a key hint"}, does: "presses the key, unless it changes a clone or asks GitHub"},
		{keys: []string{"click in a popup"}, does: "puts the cursor on an item; a click on it again chooses it"},
		{keys: []string{"click outside"}, does: "closes the popup"},
	}})
	out = append(out, helpSection{title: "Marks, set under [symbols]", rows: []helpRow{
		{keys: []string{sym.Behind}, does: "behind: commits to bring in"},
		{keys: []string{sym.Ahead}, does: "ahead: commits not pushed"},
		{keys: []string{sym.Diverged}, does: "behind and ahead; with counts, both numbers (" + sym.Behind + "2" + sym.Ahead + "1)"},
		{keys: []string{sym.Synced}, does: "even with what it follows"},
		{keys: []string{sym.BehindMain}, does: "a branch behind the default branch"},
		{keys: []string{sym.GitHubAhead}, does: "GitHub has commits not fetched yet"},
		{keys: []string{sym.Unstaged}, does: "files changed, not staged"},
		{keys: []string{sym.Staged}, does: "files staged"},
		{keys: []string{sym.Untracked}, does: "files not tracked"},
		{keys: []string{sym.Stash}, does: "stash entries"},
		{keys: []string{sym.Worktree}, does: "worktrees besides the main one"},
		{keys: []string{"⋅"}, does: "not read yet"},
	}})
	return out
}

func (m *Model) helpPopup() *popup {
	var lines []drawn
	for i, s := range helpSections(m.keys, m.actions, m.sym) {
		if i > 0 {
			lines = append(lines, drawn{})
		}
		lines = append(lines, drawn{parts: []cell{{text: s.title, role: roleSubtle, bold: true}}})
		for _, r := range s.rows {
			parts := append(cut([]cell{{text: "  " + strings.Join(r.keys, " "), role: roleAccent, bold: true}}, 24), cell{text: r.does, role: roleText})
			if r.action != "" {
				parts = append(parts, cell{text: "  " + string(r.action), role: roleDim})
			}
			lines = append(lines, drawn{parts: parts})
		}
	}
	return &popup{title: "keys", wide: true, body: &popupBody{lines: lines}}
}

// keysReference is docs/keys.md: the sections with the default keys.
func keysReference() string {
	var b strings.Builder
	b.WriteString("# Keys\n\n")
	b.WriteString("Every key gh tidy reads, with its action's name under `[keys]` in the config file ([configuration](configuration.md)), what the mouse does, and the marks. `?` shows the same in gh tidy, with the keys you set.\n\n")
	b.WriteString("This page is written from the code (`go test ./internal/tui -run TestKeysReference -update`).\n")
	d := config.Default()
	for _, s := range helpSections(keymap.Default(), nil, d.Symbols.Marks()) {
		fmt.Fprintf(&b, "\n## %s\n\n", s.title)
		named := false
		for _, r := range s.rows {
			named = named || r.action != ""
		}
		if named {
			b.WriteString("| Key | Action | Does |\n|---|---|---|\n")
		} else {
			b.WriteString("| | Does |\n|---|---|\n")
		}
		for _, r := range s.rows {
			key := strings.Join(r.keys, " ")
			if !s.words {
				ks := make([]string, len(r.keys))
				for i, k := range r.keys {
					ks[i] = "`" + k + "`"
				}
				key = strings.Join(ks, " ")
			}
			if named {
				act := ""
				if r.action != "" {
					act = "`" + string(r.action) + "`"
				}
				fmt.Fprintf(&b, "| %s | %s | %s |\n", key, act, r.does)
			} else {
				fmt.Fprintf(&b, "| %s | %s |\n", key, r.does)
			}
		}
	}
	return b.String()
}
