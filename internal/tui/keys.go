package tui

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/babarot/gh-tidy/internal/config"
)

// Operations, by the name [keys] uses.
const (
	opUp          = "up"
	opDown        = "down"
	opTop         = "top"
	opBottom      = "bottom"
	opPageUp      = "page_up"
	opPageDown    = "page_down"
	opExpand      = "expand"
	opSwitchTab   = "switch_tab"
	opSearch      = "search"
	opShowAll     = "show_all"
	opSort        = "sort"
	opOpenBrowser = "open_browser"
	opReveal      = "reveal"
	opCopyPath    = "copy_path"
	opCopyURL     = "copy_url"
	opFetch       = "fetch"
	opFetchAll    = "fetch_all"
	opPull        = "pull"
	opPullAll     = "pull_all"
	opCleanup     = "cleanup"
	opClone       = "clone"
	opHelp        = "help"
	opQuit        = "quit"
)

// defaultKeys are the keys of each operation; the order is the help's.
var defaultKeys = []struct {
	op   string
	keys []string
	help string
}{
	{opUp, []string{"up", "k"}, "move up"},
	{opDown, []string{"down", "j"}, "move down"},
	{opTop, []string{"home", "g"}, "first"},
	{opBottom, []string{"end", "G"}, "last"},
	{opPageUp, []string{"pgup", "ctrl+b"}, "page up"},
	{opPageDown, []string{"pgdown", "ctrl+f"}, "page down"},
	{opExpand, []string{"enter"}, "show or hide worktrees; pick an owner"},
	{opSwitchTab, []string{"tab"}, "cloned / not cloned"},
	{opSearch, []string{"/"}, "filter by name or branch"},
	{opShowAll, []string{"a"}, "show the hidden repositories"},
	{opSort, []string{"s"}, "change the order"},
	{opFetch, []string{"f"}, "fetch the selected repository"},
	{opFetchAll, []string{"F"}, "fetch every repository"},
	{opPull, []string{"p"}, "pull --ff-only the selected repository"},
	{opPullAll, []string{"P"}, "fetch every repository, then pull those behind with nothing in the way"},
	{opCleanup, []string{"d"}, "remove merged worktrees and branches"},
	{opClone, []string{"c"}, "clone (not cloned)"},
	{opOpenBrowser, []string{"o"}, "open on GitHub"},
	{opReveal, []string{"R"}, "reveal in Finder"},
	{opCopyPath, []string{"y"}, "copy the path"},
	{opCopyURL, []string{"Y"}, "copy the URL"},
	{opHelp, []string{"?"}, "this help"},
	{opQuit, []string{"q", "esc"}, "quit"},
}

// keymap maps a key, as tea.KeyPressMsg.String writes it, to an operation
// or a custom action ("action:<index>").
type keymap struct {
	byKey map[string]string
	byOp  map[string][]string
}

func (k keymap) op(key string) string { return k.byKey[key] }

// first returns the first key of op, for the help line.
func (k keymap) first(op string) string {
	if ks := k.byOp[op]; len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// newKeymap applies [keys] and the custom actions over the defaults. A key
// given to an operation is taken from the operation that has it by default;
// two operations written in the file cannot share one.
func newKeymap(cfg config.File) (keymap, error) {
	byOp := map[string][]string{}
	for _, d := range defaultKeys {
		byOp[d.op] = slices.Clone(d.keys)
	}
	var errs []error
	written := map[string]string{} // key -> op, for keys the file sets
	ops := make([]string, 0, len(cfg.Keys))
	for op := range cfg.Keys {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		if _, ok := byOp[op]; !ok {
			errs = append(errs, fmt.Errorf("[keys] %s is not an operation", op))
			continue
		}
		keys, err := keyList(cfg.Keys[op])
		if err != nil {
			errs = append(errs, fmt.Errorf("[keys] %s %w", op, err))
			continue
		}
		for _, k := range keys {
			if other, ok := written[k]; ok {
				errs = append(errs, fmt.Errorf("[keys] %s and %s both use %q", other, op, k))
			}
			written[k] = op
		}
		byOp[op] = keys
	}
	for i, a := range cfg.Actions {
		op := fmt.Sprintf("action:%d", i)
		if other, ok := written[a.Key]; ok {
			errs = append(errs, fmt.Errorf("[[actions]] %q and %s both use %q", a.Name, other, a.Key))
		}
		written[a.Key] = op
		byOp[op] = []string{a.Key}
	}
	// A key the file gave to one operation leaves the ones that have it by
	// default.
	for k, owner := range written {
		for op, keys := range byOp {
			if _, set := cfg.Keys[op]; op == owner || set || strings.HasPrefix(op, "action:") {
				continue
			}
			byOp[op] = slices.DeleteFunc(keys, func(x string) bool { return x == k })
		}
	}
	km := keymap{byKey: map[string]string{}, byOp: byOp}
	for op, keys := range byOp {
		for _, k := range keys {
			km.byKey[k] = op
		}
	}
	return km, errors.Join(errs...)
}

// keyList takes a key or a list of keys.
func keyList(v any) ([]string, error) {
	switch v := v.(type) {
	case string:
		return []string{v}, nil
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
