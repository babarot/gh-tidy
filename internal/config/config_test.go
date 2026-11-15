package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/babarot/gh-tidy/internal/tui/keymap"
)

func TestTemplateIsDefault(t *testing.T) {
	// Every line of the template uncommented must decode to the defaults.
	var lines []string
	example := false
	for l := range strings.SplitSeq(Template, "\n") {
		example = example || strings.HasPrefix(l, "# [[")
		if !example && strings.HasPrefix(l, "# ") && strings.Contains(l, " = ") {
			l = strings.TrimPrefix(l, "# ")
			if i := strings.Index(l, "   #"); i >= 0 {
				l = l[:i]
			}
		}
		lines = append(lines, l)
	}
	// The [[actions]] example stays commented: it is not a default.
	f, err := Parse("template", []byte(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	f.Keys = nil
	f.Actions = nil
	if !reflect.DeepEqual(f, d) {
		t.Errorf("template uncommented:\n got %+v\nwant %+v", f, d)
	}
}

// The [keys] part of the template is every action at its default keys,
// and applies without a mistake.
func TestTemplateKeysAreTheDefaults(t *testing.T) {
	var lines []string
	in := false
	for l := range strings.SplitSeq(Template, "\n") {
		switch {
		case l == "[keys]":
			in = true
		case strings.HasPrefix(l, "[") || strings.HasPrefix(l, "# [["):
			in = false
		case in && strings.HasPrefix(l, "# ") && strings.Contains(l, " = "):
			lines = append(lines, strings.TrimPrefix(l, "# "))
			continue
		}
	}
	f, err := Parse("template", []byte("[keys]\n"+strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	m, err := keymap.Apply(keymap.Default(), f.Keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Keys) != len(keymap.Defaults) {
		t.Errorf("the template has %d actions, the keymap %d", len(f.Keys), len(keymap.Defaults))
	}
	for _, d := range keymap.Defaults {
		if got := m.Keys(d.Action); !reflect.DeepEqual(got, d.Keys) {
			t.Errorf("%s = %q in the template, %q by default", d.Action, got, d.Keys)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unknown key", "[tui]\nthme = \"nord\"\n", "thme"},
		{"wrong type", "[core]\nfetch_concurrency = \"8\"\n", "x.toml:2"},
		{"bad value", "[tui]\nstatus_style = \"dots\"\n", `status_style "dots"`},
		{"bad hide", "[tui]\nhide = [\"clean\"]\n", `hide "clean"`},
		{"remove without path", "[cleanup]\nworktree_remove = [\"rm\"]\n", "{path}"},
		{"bad owner", "[owners]\nadd = [\"a/b/c\"]\n", `"a/b/c"`},
		{"no columns", "[columns]\nlocal = []\n", "[columns]"},
		{"wide symbol", "[symbols]\nstash = \"💰\"\n", "stash"},
		{"empty symbol", "[symbols]\nahead = \"\"\n", "ahead is empty"},
		{"action without run", "[[actions]]\nkey = \"H\"\nname = \"h\"\n", "#1 (h): run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("x.toml", []byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse(%q) = %v, want an error with %q", tc.src, err, tc.want)
			}
		})
	}
}

func TestLoadWritesTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh-tidy", "config.toml")
	f, err := Load(path)
	if err != nil || !reflect.DeepEqual(f, Default()) {
		t.Fatalf("Load = %+v, %v", f, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != Template {
		t.Errorf("template not written: %v", err)
	}
}

func TestExpand(t *testing.T) {
	got := Expand([]string{"herdr", "open", "{path}", "--name={name}"}, map[string]string{"path": "/a b", "name": "x"})
	want := []string{"herdr", "open", "/a b", "--name=x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %q", got)
	}
}
