package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The packages and what each may import, from the bottom layer up. A
// package imports only from lower layers and only the outside modules
// listed for it; the parts of the standard library that reach outside the
// process belong to a few packages each. docs/architecture.md shows this
// table, and TestArchitectureDoc keeps the two the same. Test files are
// not checked: a test may import what helps it.
//
// A package not in the table fails the test, so a new one is placed here
// on purpose.
var layers = []layer{
	{"internal/version", 0, nil, nil, "The release version"},
	{"internal/logging", 0, nil, nil, "The slog logger, the log file and its attribute keys"},
	{"internal/theme", 0, nil, nil, "The color schemes, named by what the TUI uses each color for"},
	{"internal/model", 0, nil, nil, "The values passed between packages: a clone's state, the facts decisions are made from, the keys of rows"},
	{"internal/gittest", 0, nil, nil, "Real git repositories in a test's temporary directory; imported by tests only"},
	{"internal/gitx", 1, nil, nil, "Running git without taking a lock, and parsing its porcelain output"},
	{"internal/policy", 1, []string{"internal/model"}, nil, "Deciding what to do with a clone from facts: moving its default branch, what d removes, how a worktree is judged, attention"},
	{"internal/config", 1, []string{"internal/theme"}, []string{"github.com/pelletier/go-toml/v2"}, "Reading the config file and reporting its mistakes"},
	{"internal/repo", 2, []string{"internal/gitx"}, nil, "One clone's state, read from git alone"},
	{"internal/local", 2, []string{"internal/gitx", "internal/model"}, nil, "A clone's state and the facts about it, read from git, and the changes made to it with git"},
	{"internal/store", 2, []string{"internal/model"}, nil, "The files kept between starts: each owner's table as last read (the cache) and the user's choices (the state)"},
	{"internal/github", 2, []string{"internal/logging", "internal/model"}, []string{"github.com/cli/go-gh/v2"}, "Reading GitHub through gh's authentication: owners' lists by REST with conditional requests, repositories' fields by GraphQL, the rate limit of every response"},
	{"internal/engine", 3, []string{"internal/github", "internal/logging", "internal/model", "internal/policy", "internal/store"}, nil, "What gh-tidy knows of owners and clones, and the only one changing it: what to read and when, limits on each kind of outside access, the rate limit budget, each clone's lane for changes; no Bubble Tea"},
	{"internal/locate", 3, []string{"internal/gitx", "internal/repo"}, nil, "The root, and what to open from where gh tidy starts"},
	{"internal/cleanup", 3, []string{"internal/config", "internal/gitx", "internal/repo"}, nil, "What d removes and keeps, and removing it"},
	{"internal/tui", 4, []string{"internal/cleanup", "internal/config", "internal/github", "internal/gitx", "internal/locate", "internal/repo", "internal/theme"},
		[]string{"charm.land/bubbletea/v2", "charm.land/lipgloss/v2", "github.com/charmbracelet/x/ansi"}, "The terminal UI"},
	{"", 5, []string{"internal/logging", "internal/config", "internal/locate", "internal/repo", "internal/tui", "internal/version"}, []string{"charm.land/bubbletea/v2"}, "Flags, config and starting the TUI"},
}

// layer is a package's place: its layer, the packages of this module
// (named by their path in it) and outside modules it may import, and what
// it does.
type layer struct {
	pkg      string
	layer    int
	local    []string
	external []string
	does     string
}

// reach are the parts of the standard library that reach outside the
// process, and the packages allowed to use each. An entry covers the
// packages under it too (net/http covers net/http/httptest), except net,
// whose subpackages such as net/url only parse text.
var reach = map[string][]string{
	"net":      {},
	"net/http": {"internal/github"},
	"net/rpc":  {},
	"net/smtp": {},
	"os/exec":  {"internal/cleanup", "internal/gittest", "internal/gitx", "internal/tui"},
	"syscall":  {},
}

const module = "github.com/babarot/gh-tidy"

type goPackage struct {
	ImportPath string
	Imports    []string
}

func listPackages(t *testing.T) []goPackage {
	t.Helper()
	out, err := exec.Command("go", "list", "-json", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []goPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p goPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func TestArchitecture(t *testing.T) {
	for _, p := range listPackages(t) {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, module), "/")
		i := slices.IndexFunc(layers, func(l layer) bool { return l.pkg == rel })
		if i < 0 {
			t.Errorf("%s is not in the layer table in arch_test.go and docs/architecture.md", p.ImportPath)
			continue
		}
		l := layers[i]
		for _, imp := range p.Imports {
			switch {
			case strings.HasPrefix(imp, module+"/"):
				dep := strings.TrimPrefix(imp, module+"/")
				if !slices.Contains(l.local, dep) {
					t.Errorf("%s imports %s, which is not in its allowed imports", p.ImportPath, imp)
				}
			case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
				if !slices.ContainsFunc(l.external, func(m string) bool { return imp == m || strings.HasPrefix(imp, m+"/") }) {
					t.Errorf("%s imports %s, an outside module not allowed for it", p.ImportPath, imp)
				}
			default:
				if key, who, ok := reaching(imp); ok && !slices.Contains(who, rel) {
					t.Errorf("%s imports %s, which only %s may use (%s)", p.ImportPath, imp, whoMay(who), key)
				}
			}
		}
	}
}

// reaching finds the reach entry that covers imp: the entry itself, or one
// above it other than net.
func reaching(imp string) (string, []string, bool) {
	for key, who := range reach {
		if imp == key || (key != "net" && strings.HasPrefix(imp, key+"/")) {
			return key, who, true
		}
	}
	return "", nil, false
}

// TestLayersGoDownward checks the table itself: every allowed import is a
// package of a lower layer.
func TestLayersGoDownward(t *testing.T) {
	layerOf := map[string]int{}
	for _, l := range layers {
		layerOf[l.pkg] = l.layer
	}
	for _, l := range layers {
		for _, dep := range l.local {
			dl, ok := layerOf[dep]
			if !ok || dl >= l.layer {
				t.Errorf("%s (layer %d) may import %s (layer %d, known %v)", pkgName(l.pkg), l.layer, dep, dl, ok)
			}
		}
	}
}

// TestNoUnusedImportAllowed checks that every package of the table exists
// and every import it allows is made, so the table tells what each
// package depends on. An outside module is used when the module or a
// package in it is imported. reach is not checked: it limits who may use
// a package, and a package no one uses is still limited.
func TestNoUnusedImportAllowed(t *testing.T) {
	imports := map[string][]string{}
	for _, p := range listPackages(t) {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, module), "/")
		imports[rel] = p.Imports
	}
	for _, l := range layers {
		imps, ok := imports[l.pkg]
		if !ok {
			t.Errorf("%s is in the table but is not a package", pkgName(l.pkg))
			continue
		}
		for _, dep := range l.local {
			if !slices.Contains(imps, module+"/"+dep) {
				t.Errorf("%s may import %s but does not; remove it from the table", pkgName(l.pkg), dep)
			}
		}
		for _, m := range l.external {
			if !slices.ContainsFunc(imps, func(imp string) bool { return imp == m || strings.HasPrefix(imp, m+"/") }) {
				t.Errorf("%s may import %s but does not; remove it from the table", pkgName(l.pkg), m)
			}
		}
	}
}

// pkgName is a package of the table as people call it.
func pkgName(pkg string) string {
	if pkg == "" {
		return "main"
	}
	return pkg
}

func whoMay(who []string) string {
	if len(who) == 0 {
		return "no package"
	}
	return strings.Join(who, ", ")
}

var update = flag.Bool("update", false, "rewrite the layer table in docs/architecture.md")

const (
	docPath  = "docs/architecture.md"
	docStart = "<!-- layers:start -->"
	docEnd   = "<!-- layers:end -->"
)

// docTable is the layer table as docs/architecture.md shows it.
func docTable() string {
	var b strings.Builder
	b.WriteString("| Layer | Package | Does | May import | Outside modules |\n|---|---|---|---|---|\n")
	join := func(ss []string) string {
		if len(ss) == 0 {
			return "-"
		}
		q := make([]string, len(ss))
		for i, s := range ss {
			q[i] = "`" + s + "`"
		}
		return strings.Join(q, ", ")
	}
	for _, l := range layers {
		fmt.Fprintf(&b, "| %d | `%s` | %s | %s | %s |\n", l.layer, pkgName(l.pkg), l.does, join(l.local), join(l.external))
	}
	b.WriteString("\nStandard library packages that reach outside the process:\n\n| Package | Only in |\n|---|---|\n")
	names := make([]string, 0, len(reach))
	for n := range reach {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(&b, "| `%s` | %s |\n", n, join(reach[n]))
	}
	return b.String()
}

func TestArchitectureDoc(t *testing.T) {
	src, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	start, end := strings.Index(doc, docStart), strings.Index(doc, docEnd)
	if start < 0 || end < start {
		t.Fatalf("%s has no %s ... %s block", docPath, docStart, docEnd)
	}
	want := docStart + "\n" + docTable() + docEnd
	got := doc[start : end+len(docEnd)]
	if got == want {
		return
	}
	if *update {
		if err := os.WriteFile(docPath, []byte(doc[:start]+want+doc[end+len(docEnd):]), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("the layer table in %s differs from arch_test.go; run go test -run TestArchitectureDoc -update", docPath)
}
