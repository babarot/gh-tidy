package config

import (
	"flag"
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
		{"wrong type", "[core]\nroots = 8\n", "x.toml:2"},
		{"root taken out", "[core]\nroot = \"~/src\"\n", `[core] root is no longer a setting: the places of clones are a list: write roots = ["<the same path>"]`},
		{"relative root", "[core]\nroots = [\"src\"]\n", `"src" is not absolute or under ~`},
		{"root twice", "[core]\nroots = [\"~/src\", \"~/src\"]\n", "written twice"},
		{"bad theme", "[tui]\ntheme = \"dots\"\n", `theme "dots"`},
		{"removed", "[tui]\nsort = \"name\"\n", "[tui] sort is no longer a setting: the list is in the order"},
		{"removed in core", "[core]\nstale_check = false\n", "stale_check is no longer a setting"},
		{"remove without path", "[cleanup]\nworktree_remove = [\"rm\"]\n", "{path}"},
		{"bad owner", "[owners]\nadd = [\"a/b/c\"]\n", `"a/b/c"`},
		{"owner on another host", "[owners]\nadd = [\"ghe.example.com/acme\"]\n", "github.com only"},
		{"no columns", "[columns]\nlocal = []\n", "[columns]"},
		{"wide symbol", "[symbols]\nstash = \"💰\"\n", "stash"},
		{"action without run", "[[actions]]\nkey = \"H\"\nname = \"h\"\n", "#1 (h): run"},
		{"section without name", "[[sections]]\nrepos = [\"x\"]\n", "[[sections]] #1: name is missing"},
		{"section twice", "[[sections]]\nname = \"a\"\nrepos = [\"x\"]\n[[sections]]\nname = \"A\"\nrepos = [\"y\"]\n", "another section"},
		{"section of gh tidy", "[[sections]]\nname = \"not cloned\"\nrepos = [\"x\"]\n", "gh tidy makes itself"},
		{"section without repos", "[[sections]]\nname = \"a\"\n", "repos must name"},
		{"bad pattern", "[[sections]]\nname = \"a\"\nrepos = [\"[x\"]\n", `"[x" is not a pattern`},
		{"pattern of a host", "[[sections]]\nname = \"a\"\nrepos = [\"github.com/acme/x\"]\n", "is not a pattern"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("x.toml", []byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse(%q) = %v, want an error with %q", tc.src, err, tc.want)
			}
		})
	}
}

func TestLoadWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh-tidy", "config.toml")
	f, err := Load(path)
	if err != nil || !reflect.DeepEqual(f, Default()) {
		t.Fatalf("Load = %+v, %v", f, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a file was written")
	}
}

// docs/configuration.md shows the template; go test -run TestConfigurationDoc
// -update writes it again.
var update = flag.Bool("update", false, "rewrite docs/configuration.md")

func TestConfigurationDoc(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "configuration.md")
	want := "# Configuration\n\n" +
		"gh tidy reads `~/.config/gh-tidy/config.toml` (or `$XDG_CONFIG_HOME/gh-tidy/config.toml`). Every setting is optional: one left out takes its default. A mistake is reported with where it is in the file, and a setting that is no longer read says why.\n\n" +
		"This page is written from the code (`go test ./internal/config -run TestConfigurationDoc -update`). `gh tidy --print-config` prints the same, to start a file from.\n\n" +
		"```toml\n" + Template + "```\n"
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("docs/configuration.md is not the template; run go test ./internal/config -run TestConfigurationDoc -update")
	}
}

func TestExpand(t *testing.T) {
	got := Expand([]string{"herdr", "open", "{path}", "--name={name}"}, map[string]string{"path": "/a b", "name": "x"})
	want := []string{"herdr", "open", "/a b", "--name=x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %q", got)
	}
}
