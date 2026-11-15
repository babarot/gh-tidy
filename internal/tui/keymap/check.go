package keymap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// namedKeys are the keys besides a single character, as Bubble Tea writes
// key presses: space is "space", not " ".
var namedKeys = map[string]bool{
	"enter": true, "space": true, "tab": true, "backspace": true, "esc": true,
	"up": true, "down": true, "left": true, "right": true,
	"home": true, "end": true, "pgup": true, "pgdown": true, "insert": true, "delete": true,
}

func init() {
	for i := 1; i <= 12; i++ {
		namedKeys[fmt.Sprintf("f%d", i)] = true
	}
}

// modifiers are the modifiers, in the order Bubble Tea writes them.
var modifiers = []string{"ctrl", "alt", "shift"}

// CheckKey reports why a key written in the config file would never match
// a key press, with how to write it instead when there is one.
func CheckKey(k string) error {
	if why, ok := Fixed[k]; ok {
		return fmt.Errorf("%s is fixed: it %s", k, why)
	}
	if k == "" || k == " " {
		return errors.New(`an empty key; write space as "space"`)
	}
	// Split off the modifiers; a + at the end is the + key (ctrl++).
	var mods []string
	base := k
	for {
		i := strings.Index(base, "+")
		if i <= 0 || i == len(base)-1 {
			break
		}
		mods, base = append(mods, base[:i]), base[i+1:]
	}
	if len(mods) == 0 && len(k) > 1 && strings.Contains(k, "-") {
		if alt := strings.ReplaceAll(k, "-", "+"); CheckKey(alt) == nil {
			return fmt.Errorf("%q is not a key; write %q", k, alt)
		}
	}
	has := map[string]bool{}
	for _, m := range mods {
		if !slices.Contains(modifiers, m) || has[m] {
			return fmt.Errorf("%q is not a key: %q is not one of ctrl, alt, shift, each once", k, m)
		}
		has[m] = true
	}
	var ordered []string
	for _, m := range modifiers {
		if has[m] {
			ordered = append(ordered, m)
		}
	}
	join := func(mods []string, base string) string { return strings.Join(append(slices.Clone(mods), base), "+") }
	if !namedKeys[base] {
		if lower := strings.ToLower(base); namedKeys[lower] {
			return fmt.Errorf("%q is not a key; write %q", k, join(ordered, lower))
		}
		r, size := utf8.DecodeRuneInString(base)
		if size != len(base) || !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%q is not a key", k)
		}
		switch {
		case len(ordered) == 1 && has["shift"]:
			// A shifted character is read as the character: shift+y is Y.
			return fmt.Errorf("%q is never read: a shifted character arrives as itself; write %q", k, string(unicode.ToUpper(r)))
		case len(ordered) > 0 && unicode.IsUpper(r):
			// With ctrl or alt, a letter is read lower-cased, with shift+
			// for the shift key: ctrl+Y arrives as ctrl+shift+y.
			want := ordered
			if !has["shift"] {
				want = append(slices.Clone(ordered), "shift")
				slices.SortFunc(want, func(a, b string) int { return slices.Index(modifiers, a) - slices.Index(modifiers, b) })
			}
			return fmt.Errorf("%q is never read; write %q", k, join(want, string(unicode.ToLower(r))))
		}
	}
	if !slices.Equal(mods, ordered) {
		return fmt.Errorf("%q is never read; write %q", k, join(ordered, base))
	}
	return nil
}
