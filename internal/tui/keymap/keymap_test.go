package keymap

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultsHaveNoConflicts(t *testing.T) {
	m := Default()
	if cs := m.conflicts(); len(cs) > 0 {
		t.Fatalf("default keys conflict: %+v", cs)
	}
	seen := map[Action]bool{}
	for _, d := range Defaults {
		if seen[d.Action] {
			t.Errorf("action %q defined twice", d.Action)
		}
		seen[d.Action] = true
		if d.Help == "" || d.Doc == "" {
			t.Errorf("action %q lacks its hint or what it does", d.Action)
		}
		for _, k := range d.Keys {
			if err := CheckKey(k); err != nil {
				t.Errorf("default key of %s: %v", d.Action, err)
			}
		}
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		set     map[string]any
		check   map[Action][]string
		problem string
	}{
		{
			name:  "replace keys",
			set:   map[string]any{"fetch": "a"},
			check: map[Action][]string{"fetch": {"a"}},
		},
		{
			name:  "a list of keys",
			set:   map[string]any{"fetch": []any{"a", "b"}},
			check: map[Action][]string{"fetch": {"a", "b"}},
		},
		{
			name:  "turn off",
			set:   map[string]any{"cleanup_all": []any{}},
			check: map[Action][]string{"cleanup_all": {}},
		},
		{
			name:  "set key takes it from a default nav key",
			set:   map[string]any{"fetch": "j"},
			check: map[Action][]string{"fetch": {"j"}, "down": {"down", "ctrl+n"}},
		},
		{
			name:  "scopes that do not meet keep their keys",
			set:   map[string]any{"fetch": "y"},
			check: map[Action][]string{"fetch": {"y"}, "confirm": {"y", "enter"}},
		},
		{
			name:    "two set actions on one key",
			set:     map[string]any{"fetch": "x", "pull": "x"},
			problem: `[keys] pull: "x" is also fetch's key`,
		},
		{
			name:    "unknown action",
			set:     map[string]any{"fetchh": "x"},
			problem: `"fetchh" is not an action; did you mean "fetch"?`,
		},
		{
			name:    "removed action",
			set:     map[string]any{"switch_tab": "tab"},
			problem: "no longer an action",
		},
		{
			name:    "shifted letter",
			set:     map[string]any{"fetch": "shift+a"},
			problem: `write "A"`,
		},
		{
			name:    "fixed key",
			set:     map[string]any{"quit": "esc"},
			problem: "fixed",
		},
		{
			name:    "not a string",
			set:     map[string]any{"quit": int64(1)},
			problem: "as strings",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := Apply(Default(), tt.set)
			if tt.problem != "" {
				if err == nil || !strings.Contains(err.Error(), tt.problem) {
					t.Fatalf("err = %v; want one containing %q", err, tt.problem)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for a, want := range tt.check {
				if got := m.Keys(a); !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
					t.Errorf("Keys(%s) = %q; want %q", a, got, want)
				}
			}
		})
	}
}

func TestMatches(t *testing.T) {
	m := Default()
	if !m.Matches("pull_all", "P") || m.Matches("pull_all", "p") {
		t.Error("pull_all should match P only")
	}
	if m.First("lens") != "v" {
		t.Errorf("First(lens) = %q", m.First("lens"))
	}
	if !Known("lens") || Known("switch_tab") {
		t.Error("Known")
	}
}

func TestCheckKey(t *testing.T) {
	tests := []struct {
		key  string
		want string // empty when the key is fine
	}{
		{"a", ""},
		{"A", ""},
		{"ctrl+s", ""},
		{"shift+tab", ""},
		{"+", ""},
		{"f5", ""},
		{"Enter", `write "enter"`},
		{"ctrl-s", `write "ctrl+s"`},
		{"shift+y", `write "Y"`},
		{"ctrl+Y", `write "ctrl+shift+y"`},
		{"hyper+a", "not one of"},
		{" ", "empty"},
		{"ab", "not a key"},
	}
	for _, tt := range tests {
		err := CheckKey(tt.key)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("CheckKey(%q) = %v; want nil", tt.key, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("CheckKey(%q) = %v; want one containing %q", tt.key, err, tt.want)
		}
	}
}
