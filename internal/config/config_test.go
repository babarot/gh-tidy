package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTemplateIsDefault(t *testing.T) {
	// Every line of the template uncommented must decode to the defaults.
	var lines []string
	for l := range strings.SplitSeq(Template, "\n") {
		if strings.HasPrefix(l, "# ") && strings.Contains(l, " = ") && !strings.HasPrefix(l, "# [[") {
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

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"unknown key", "[tui]\nthme = \"nord\"\n", "thme"},
		{"wrong type", "[core]\nfetch_concurrency = \"8\"\n", "x.toml:2"},
		{"bad value", "[tui]\nstatus_style = \"dots\"\n", `status_style "dots"`},
		{"bad hide", "[tui]\nhide = [\"clean\"]\n", `hide "clean"`},
		{"remove without path", "[cleanup]\nworktree_remove = [\"rm\"]\n", "{path}"},
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
