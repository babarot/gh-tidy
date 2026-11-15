package tui

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/engine"
	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/tui/columns"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// Every name, repository and value here is made up.
var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

const root = "/src"

func ago(d time.Duration) time.Time { return now.Add(-d) }

// fakeLocal is the clones on disk, as values.
type fakeLocal struct {
	mu     sync.Mutex
	clones map[string]model.Clone
	facts  map[string]model.CleanupFacts
}

func (f *fakeLocal) add(c model.Clone, facts *model.CleanupFacts) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clones[c.Path] = c
	if facts != nil {
		f.facts[c.Path] = *facts
	}
}

func (f *fakeLocal) Owners(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.clones {
		if o := filepath.Base(filepath.Dir(p)); filepath.Dir(filepath.Dir(p)) == dir && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeLocal) Find(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.clones {
		if filepath.Dir(p) == dir {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeLocal) Scan(_ context.Context, path string) model.Clone {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clones[path]
}

func (f *fakeLocal) SetDefault(_ context.Context, c model.Clone, name string) model.Clone {
	c.Default, c.DefaultSource = name, model.DefaultFromGitHub
	return c
}

func (f *fakeLocal) RemoteAhead(_ context.Context, path, _, oid string) model.Ahead {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.clones[path]
	return model.Ahead{Remote: oid, Local: c.DefaultOid, Ahead: oid != c.DefaultOid}
}

func (f *fakeLocal) WorktreeFacts(_ context.Context, c model.Clone) (model.CleanupFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.facts[c.Path], nil
}

func (f *fakeLocal) Detail(_ context.Context, c model.Clone, tree string) model.Detail {
	if tree != "" {
		return model.Detail{Unpushed: []model.Commit{{Hash: "ef81585", Subject: "Speed up status parsing", Author: "ren", When: ago(7 * time.Hour)}}}
	}
	d := model.Detail{}
	if c.Name == "agent-runner" {
		d.Incoming = []model.Commit{
			{Hash: "d3665f0", Subject: "Bump golang.org/x/net from 0.45.0 to 0.47.0", Author: "hana", When: ago(time.Hour)},
			{Hash: "f02d507", Subject: "Document the cache layout", Author: "kai", When: ago(2 * time.Hour)},
		}
		d.Unpushed = []model.Commit{{Hash: "ef81585", Subject: "Speed up status parsing", Author: "ren", When: ago(7 * time.Hour)}}
		d.Files = []model.File{{XY: " M", Path: "internal/retry.go"}}
	}
	return d
}

// fakeGitHub is GitHub, as values.
type fakeGitHub struct {
	lists map[string]github.Listing
	heavy map[string]github.HeavyRepo
}

func (f *fakeGitHub) Host() string { return "github.com" }

func (f *fakeGitHub) Viewer(context.Context) (github.Viewer, github.Rate, error) {
	return github.Viewer{Login: "mika", Orgs: []string{"acme"}}, github.Rate{}, nil
}

func (f *fakeGitHub) List(_ context.Context, owner string, _ github.OwnerKind, _ github.Pages, _ int) (github.Listing, error) {
	return f.lists[strings.ToLower(owner)], nil
}

func (f *fakeGitHub) Heavy(_ context.Context, slugs []string, _ github.Want) ([]github.HeavyRepo, github.Rate, error) {
	var out []github.HeavyRepo
	for _, s := range slugs {
		k := model.NewRemoteKey("github.com", s)
		h, ok := f.heavy[strings.ToLower(s)]
		if !ok {
			out = append(out, github.HeavyRepo{Asked: k, Key: k})
			continue
		}
		h.Asked, h.Key, h.Found = k, k, true
		out = append(out, h)
	}
	return out, github.Rate{Cost: 1}, nil
}

// clone is a clone of mika/name on main, up to date, fetched an hour ago.
func clone(name string, f func(*model.Clone)) model.Clone {
	path := filepath.Join(root, "github.com", "mika", name)
	slug := "mika/" + name
	c := model.Clone{Key: model.NewCloneKey(path), Path: path, Name: name, OriginURL: "git@github.com:" + slug + ".git",
		Host: "github.com", Slug: slug, Remote: model.NewRemoteKey("github.com", slug),
		Default: "main", DefaultSource: model.DefaultFromOriginHEAD, DefaultOid: "oid-" + name, FetchedAt: ago(time.Hour),
		Status:    model.Status{Head: "main", Upstream: "origin/main", HasAB: true},
		Worktrees: []model.Worktree{{Path: path, Branch: "main", Main: true}},
		Branches:  []model.Branch{{Name: "main", Upstream: "origin/main"}}}
	if f != nil {
		f(&c)
	}
	return c
}

type world struct {
	t      *testing.T
	local  *fakeLocal
	gh     *fakeGitHub
	change *fakeChange
	cmds   *fakeCommands
	eng    *engine.Engine
}

// fakeChange records the changes asked of git and gh, and makes them.
type fakeChange struct {
	mu    sync.Mutex
	local *fakeLocal
	calls []string
}

func (f *fakeChange) record(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeChange) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
func (f *fakeChange) Fetch(_ context.Context, path string) error {
	f.record("fetch " + filepath.Base(path))
	return nil
}
func (f *fakeChange) AdvanceFacts(_ context.Context, path, name string, src model.DefaultSource) model.AdvanceFacts {
	return model.AdvanceFacts{Default: name, DefaultSource: src, LocalExists: true, RemoteExists: true, Upstream: "origin/" + name, Behind: 2, FastForward: true}
}
func (f *fakeChange) FastForwardRef(_ context.Context, path, name string) error {
	f.record("ff " + filepath.Base(path))
	return nil
}
func (f *fakeChange) MergeFastForward(context.Context, string, string) error { return nil }
func (f *fakeChange) CleanupFacts(_ context.Context, c model.Clone) (model.CleanupFacts, error) {
	return f.local.WorktreeFacts(context.Background(), c)
}
func (f *fakeChange) WorktreeDirty(context.Context, string) (int, error) { return 0, nil }
func (f *fakeChange) RemoveWorktree(_ context.Context, path, wt, branch string) error {
	f.record("remove " + wt)
	return nil
}
func (f *fakeChange) BranchExists(context.Context, string, string) bool                { return false }
func (f *fakeChange) DeleteMergedBranch(context.Context, string, string, string) error { return nil }
func (f *fakeChange) DeleteBranch(context.Context, string, string) error               { return nil }
func (f *fakeChange) PruneWorktrees(context.Context, string) error                     { return nil }
func (f *fakeChange) Clone(_ context.Context, host, slug, dir string) error {
	f.record("clone " + slug + " " + dir)
	f.local.add(clone(filepath.Base(dir), nil), nil)
	return nil
}

// fakeCommands records the programs the screen would run.
type fakeCommands struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeCommands) record(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeCommands) Run(_ context.Context, dir string, argv []string) (string, error) {
	f.record("run " + dir + " " + strings.Join(argv, " "))
	return "ok\nall done", nil
}
func (f *fakeCommands) Interactive(dir string, argv []string) tea.ExecCommand { return nil }
func (f *fakeCommands) OpenURL(u string) error                                { f.record("open " + u); return nil }
func (f *fakeCommands) Reveal(p string) error                                 { f.record("reveal " + p); return nil }
func (f *fakeCommands) Copy(t string) error                                   { f.record("copy " + t); return nil }

func newWorld(t *testing.T) *world {
	w := &world{t: t, local: &fakeLocal{clones: map[string]model.Clone{}, facts: map[string]model.CleanupFacts{}},
		gh: &fakeGitHub{lists: map[string]github.Listing{}, heavy: map[string]github.HeavyRepo{}}}
	l := w.local
	l.add(clone("agent-runner", func(c *model.Clone) {
		base := c.Path
		c.Head, c.Upstream, c.Ahead, c.BaseBehind, c.Unstaged = "retry-backoff", "origin/retry-backoff", 2, 5, 1
		c.FetchedAt = ago(5 * time.Hour)
		c.Branches = []model.Branch{{Name: "main", Upstream: "origin/main", Behind: 6},
			{Name: "retry-backoff", Upstream: "origin/retry-backoff", Ahead: 2},
			{Name: "fix-cgo", Upstream: "origin/fix-cgo", Ahead: 1}, {Name: "try-ui"}, {Name: "landed"}}
		c.Worktrees = []model.Worktree{{Path: base, Branch: "retry-backoff", Main: true},
			{Path: "/wt/fix-cgo", Branch: "fix-cgo", Active: ago(3 * time.Hour)},
			{Path: "/wt/try-ui", Branch: "try-ui", Created: ago(20 * time.Minute)},
			{Path: "/wt/landed", Branch: "landed", Active: ago(2 * 24 * time.Hour)}}
	}), &model.CleanupFacts{Merged: map[string]bool{"main": true, "try-ui": true, "landed": true},
		WorkedOn: map[string]bool{"fix-cgo": true, "landed": true}})
	l.add(clone("scratchpad", func(c *model.Clone) {
		c.OriginURL, c.Slug, c.Remote, c.Untracked = "", "", "", 4
		c.Branches[0].Upstream, c.Upstream = "", ""
	}), nil)
	l.add(clone("dotfiles", func(c *model.Clone) {
		c.Branches[0].Behind, c.Unstaged, c.Untracked, c.Stash = 2, 3, 2, 1
	}), nil)
	l.add(clone("calendar-sync", func(c *model.Clone) {
		c.Head, c.Upstream, c.BaseBehind, c.Unstaged, c.Staged = "feat/ics-export", "origin/feat/ics-export", 1, 1, 2
		c.Branches = []model.Branch{{Name: "main", Upstream: "origin/main", Behind: 1}, {Name: "feat/ics-export", Upstream: "origin/feat/ics-export"}}
		c.Worktrees[0].Branch = "feat/ics-export"
	}), nil)
	l.add(clone("color-picker", nil), nil)
	l.add(clone("trail-notes", nil), nil)
	l.add(clone("pixel-garden", nil), nil)
	listed := []github.ListedRepo{}
	for _, r := range []struct {
		name            string
		fork, archived  bool
		private         bool
		pushed          time.Duration
		issues, prs, al int
		ci              string
	}{
		{"agent-runner", false, false, false, 2 * time.Hour, 4, 2, 0, "SUCCESS"},
		{"dotfiles", false, false, true, 3 * time.Hour, 0, 0, 0, "SUCCESS"},
		{"calendar-sync", false, false, false, 3 * 24 * time.Hour, 1, 0, 2, "FAILURE"},
		{"color-picker", false, false, false, 15 * 24 * time.Hour, 0, 0, 0, ""},
		{"trail-notes", false, false, false, 24 * time.Hour, 0, 1, 0, "PENDING"},
		{"pixel-garden", false, false, false, 20 * time.Hour, 0, 0, 0, "SUCCESS"},
		{"tool-shed", false, false, false, 4 * 24 * time.Hour, 3, 0, 1, "SUCCESS"},
		{"weather-cli", false, false, true, 9 * 24 * time.Hour, 0, 0, 0, ""},
		{"forked-lib", true, false, false, time.Hour, 0, 0, 0, ""},
		{"old-site", false, true, false, 20 * 24 * time.Hour, 0, 0, 0, ""},
	} {
		slug := "mika/" + r.name
		listed = append(listed, github.ListedRepo{Key: model.NewRemoteKey("github.com", slug), Name: r.name, NameWithOwner: slug,
			Fork: r.fork, Archived: r.archived, Private: r.private, PushedAt: ago(r.pushed), DefaultBranch: "main"})
		oid := "oid-" + r.name
		if r.name == "trail-notes" {
			oid = "newer"
		}
		w.gh.heavy[slug] = github.HeavyRepo{PushedAt: ago(r.pushed), Default: "main", DefaultOid: oid, CI: r.ci,
			Issues: r.issues, PRs: r.prs, Alerts: r.al, AlertsOff: r.name == "weather-cli"}
	}
	w.gh.lists["mika"] = github.Listing{Repos: listed, Completeness: github.Complete,
		Pages: github.Pages{ETags: []string{"e1"}, Ended: true, Completeness: github.Complete}}
	w.gh.lists["acme"] = github.Listing{Completeness: github.Complete, Pages: github.Pages{ETags: []string{"e2"}, Ended: true, Completeness: github.Complete}}
	w.change = &fakeChange{local: w.local}
	w.cmds = &fakeCommands{}
	w.eng = engine.New(engine.Options{Root: root, Local: w.local, GitHub: w.gh, Change: w.change, Cloner: w.change,
		Now: func() time.Time { return now }})
	if err := w.eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) model(edit func(*Options)) *Model {
	o := Options{Engine: w.eng, Commands: w.cmds, Config: config.Default(), Owners: []string{"acme", "mika"}, Owner: "mika",
		Home: "/src", Now: func() time.Time { return now }, NoTimers: true}
	if edit != nil {
		edit(&o)
	}
	m, err := New(o)
	if err != nil {
		w.t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 38})
	m.Init()
	w.settle(m)
	return m
}

// settle waits for the engine to have nothing left to do, and hands the
// screen what changed, until a change asks for nothing more.
func (w *world) settle(m *Model) {
	for range 5 {
		if err := w.eng.Idle(context.Background()); err != nil {
			w.t.Fatal(err)
		}
		m.Update(changedMsg{})
	}
}

func press(w *world, m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "backspace":
			msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
		default:
			r := []rune(k)[0]
			msg = tea.KeyPressMsg{Code: r, Text: k}
		}
		_, cmd := m.Update(msg)
		w.run(m, cmd)
		w.settle(m)
	}
}

// run runs cmd and what it leads to, feeding the screen's own messages
// back; the timers are off, so every command returns.
func (w *world) run(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case sayMsg, planMsg, actionDoneMsg, changedMsg:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func screen(m *Model) string {
	ls := m.screen()
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = strings.TrimRight(plain(l), " ")
	}
	return strings.Join(out, "\n") + "\n"
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run with -update to accept):\n%s", name, got)
	}
}

func TestLocal(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	golden(t, "local", screen(m))
	press(w, m, "enter")
	golden(t, "local-worktrees", screen(m))
}

func TestGitHub(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	press(w, m, "v")
	golden(t, "github", screen(m))
}

func TestNoCounts(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) { o.Config.Symbols.Counts = false })
	golden(t, "local-no-counts", screen(m))
}

func TestEveryColumnAndSectionIsDrawn(t *testing.T) {
	d := config.Default()
	for _, c := range columns.All {
		if !slices.Contains(d.Columns.Local, c.Name) && !slices.Contains(d.Columns.GitHub, c.Name) {
			t.Errorf("column %s is in no default set, so no golden draws it", c.Name)
		}
	}
	for _, s := range columns.Sections {
		if !slices.Contains(d.Preview.Sections, s.Name) {
			t.Errorf("section %s is not shown by default, so no golden draws it", s.Name)
		}
	}
}

func TestOwners(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	press(w, m, ">")
	if m.owner != "all" || !strings.Contains(screen(m), "GitHub as last read") {
		t.Errorf("after >: %s\n%s", m.owner, screen(m))
	}
	press(w, m, "O", "a", "c", "enter")
	if m.owner != "acme" {
		t.Errorf("chose %q", m.owner)
	}
	if s := screen(m); !strings.Contains(s, "0 clones") {
		t.Errorf("an owner with nothing:\n%s", s)
	}
	press(w, m, "<")
	if m.owner != "all" {
		t.Errorf("after <: %s", m.owner)
	}
}

func TestFilter(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	press(w, m, "/", "d", "o", "t", "enter")
	s := screen(m)
	if !strings.Contains(s, "dotfiles") || strings.Contains(s, "agent-runner") || !strings.Contains(s, " / dot") {
		t.Errorf("filtered:\n%s", s)
	}
	press(w, m, "esc")
	if !strings.Contains(screen(m), "agent-runner") {
		t.Error("esc did not clear the filter")
	}
}

func TestFoldsAndMouse(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	// The cursor goes to the head of not cloned, which enter opens.
	for i, l := range m.lines {
		if l.head == headNotCloned {
			m.moveTo(i)
		}
	}
	press(w, m, "enter")
	if !strings.Contains(screen(m), "tool-shed") {
		t.Errorf("not cloned did not open:\n%s", screen(m))
	}
	// A click on github in the header switches the set.
	m.screen()
	for _, h := range m.hits {
		if h.y == 0 && h.x1-h.x0 == len("github") {
			m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: h.x0, Y: 0})
		}
	}
	if m.lens != columns.GitHub {
		t.Error("a click on github did not switch the set")
	}
}

func TestSavesWhatTheNextStartOpens(t *testing.T) {
	w := newWorld(t)
	var saved []string
	m := w.model(func(o *Options) {
		o.Save = func(owner, lens string, ph int) {
			saved = append(saved, owner+" "+lens+" "+strings.Repeat("+", ph-previewDefault))
		}
	})
	press(w, m, "v", "+", ">")
	if want := []string{"mika github ", "mika github +", "all github +"}; !slices.Equal(saved, want) {
		t.Errorf("saved %q, want %q", saved, want)
	}
}

func TestBadSettings(t *testing.T) {
	w := newWorld(t)
	cfg := config.Default()
	cfg.Columns.Local = []string{"repo", "nope"}
	cfg.Preview.Sections = []string{"what"}
	_, err := New(Options{Engine: w.eng, Config: cfg})
	if err == nil || !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "what") {
		t.Errorf("err = %v", err)
	}
}

func TestPaneKeysTurnedOff(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) {
		o.Config.Keys = map[string]any{"preview_grow": []any{}, "preview_shrink": []any{}}
	})
	if s := screen(m); strings.Contains(s, " preview") {
		t.Errorf("a hint without keys:\n%s", s)
	}
}

func TestTheQuietHeadStaysWhileRowsHold(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.folds[headQuiet] = false
	m.relayout()
	// Rows hold their places while something is read.
	m.snap.Reading = "reading GitHub"
	m.order = map[string]int{}
	m.relayout()
	m.relayout()
	if !strings.Contains(screen(m), "▾ nothing to do") {
		t.Errorf("the head went away:\n%s", screen(m))
	}
}

func (w *world) to(m *Model, key string) {
	w.t.Helper()
	for i, l := range m.lines {
		if l.key() == key {
			m.moveTo(i)
			return
		}
	}
	w.t.Fatalf("no row %s", key)
}

const runner = "/src/github.com/mika/agent-runner"

func TestFetchAndClearResults(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	w.to(m, runner)
	press(w, m, "f")
	row := func() string {
		for l := range strings.SplitSeq(screen(m), "\n") {
			if strings.Contains(l, "agent-runner ⌥") {
				return l
			}
		}
		return ""
	}
	if !slices.Contains(w.change.seen(), "fetch agent-runner") || !strings.Contains(row(), " fetched ") {
		t.Fatalf("calls %v\n%s", w.change.seen(), screen(m))
	}
	press(w, m, "esc")
	if strings.Contains(row(), " fetched ") {
		t.Errorf("esc did not put the results away: %s", row())
	}
}

func TestUpdateAllAsksFirst(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	press(w, m, "P")
	if m.popup == nil || !strings.Contains(strings.Join(popupText(m), "\n"), "can update") {
		t.Fatalf("no confirm:\n%s", strings.Join(popupText(m), "\n"))
	}
	press(w, m, "esc")
	if len(w.change.seen()) != 0 {
		t.Fatalf("esc ran it: %v", w.change.seen())
	}
	press(w, m, "P", "y")
	if n := len(slices.DeleteFunc(w.change.seen(), func(s string) bool { return !strings.HasPrefix(s, "ff ") })); n != 6 {
		t.Errorf("%d fast-forwards: %v", n, w.change.seen())
	}
}

func TestCleanupShowsThePlan(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	w.to(m, runner)
	press(w, m, "d")
	text := strings.Join(popupText(m), "\n")
	if !strings.Contains(text, "remove ⌥ /wt/landed") || !strings.Contains(text, "keep") {
		t.Fatalf("plan:\n%s", text)
	}
	press(w, m, "y")
	if !slices.Contains(w.change.seen(), "remove /wt/landed") || slices.Contains(w.change.seen(), "remove /wt/try-ui") {
		t.Errorf("calls %v", w.change.seen())
	}
}

func TestCloneOpenCopyAndActions(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) {
		o.Config.Actions = []config.Action{{Key: "H", Name: "hello", Run: []string{"echo", "{repo}", "{branch}"}, Confirm: true}}
	})
	for i, l := range m.lines {
		if l.head == headNotCloned {
			m.moveTo(i)
		}
	}
	press(w, m, "enter")
	w.to(m, "github.com/mika/tool-shed")
	press(w, m, "c")
	if !slices.Contains(w.change.seen(), "clone mika/tool-shed /src/github.com/mika/tool-shed") {
		t.Fatalf("calls %v", w.change.seen())
	}
	if !strings.Contains(screen(m), "cloned") {
		t.Errorf("not cloned:\n%s", screen(m))
	}
	w.to(m, runner)
	press(w, m, "o", "y", "H")
	if m.popup == nil || !strings.Contains(strings.Join(popupText(m), "\n"), "echo mika/agent-runner retry-backoff") {
		t.Fatalf("action confirm:\n%s", strings.Join(popupText(m), "\n"))
	}
	press(w, m, "enter")
	want := []string{"open https://github.com/mika/agent-runner", "copy " + runner, "run " + runner + " echo mika/agent-runner retry-backoff"}
	if got := w.cmds.calls; !slices.Equal(got, want) {
		t.Errorf("commands %q, want %q", got, want)
	}
	if !strings.Contains(screen(m), "hello: all done") {
		t.Errorf("no result:\n%s", screen(m))
	}
}

func TestAnActionOnAKeyTaken(t *testing.T) {
	w := newWorld(t)
	cfg := config.Default()
	cfg.Actions = []config.Action{{Key: "f", Name: "x", Run: []string{"x"}}}
	if _, err := New(Options{Engine: w.eng, Config: cfg}); err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Errorf("err = %v", err)
	}
}

func popupText(m *Model) []string {
	if m.popup == nil {
		return nil
	}
	var out []string
	for _, l := range m.popupLines() {
		out = append(out, plain(l))
	}
	return out
}

func TestTheCursorWaitsForTheRowsBeingRead(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) { o.Select = "/src/github.com/mika/dotfiles" })
	if l, _ := m.selected(); l.key() != "/src/github.com/mika/dotfiles" {
		t.Errorf("started on %s", l.key())
	}
	// Before anything is read only a head shows; the first clone read
	// comes above it, and the cursor goes with the top.
	m2 := w.model(nil)
	m2.snap = model.Owner{List: model.ListPartial}
	m2.relayout()
	m2.snap = w.eng.Owner()
	m2.relayout()
	if m2.sel != 0 {
		t.Errorf("the cursor stayed on line %d", m2.sel)
	}
}

func TestQuitWaitsForChanges(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	if !w.eng.Hold(runner, "running x") {
		t.Fatal("no hold")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd != nil || !m.quitting || !strings.Contains(screen(m), "quitting once 1 change ends") {
		t.Fatalf("quit with a change in flight:\n%s", screen(m))
	}
	w.eng.Release(runner)
	if err := w.eng.Idle(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, cmd = m.Update(changedMsg{})
	if cmd == nil {
		t.Fatal("did not quit once the change ended")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("not a quit")
	}
}

func TestAStalePlanIsNotShown(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	w.to(m, runner)
	_, first := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	press(w, m, "P") // another popup opens while the plan is read
	var msg planMsg
	switch got := first().(type) {
	case planMsg:
		msg = got
	case tea.BatchMsg:
		for _, c := range got {
			if c == nil {
				continue
			}
			if p, ok := c().(planMsg); ok {
				msg = p
			}
		}
	}
	if msg.seq == 0 {
		t.Fatal("no plan read")
	}
	m.Update(msg)
	if !strings.Contains(strings.Join(popupText(m), "\n"), "update all") {
		t.Errorf("the plan replaced the popup open:\n%s", strings.Join(popupText(m), "\n"))
	}
}

func TestALongConfirmKeepsItsKeysInSight(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	var body []drawn
	for i := range 100 {
		body = append(body, drawn{parts: []cell{{text: fmt.Sprintf("remove branch-%d", i)}}})
	}
	m.popup = m.confirmPopup("clean up", body, func(*Model) tea.Cmd { return nil })
	lines := popupText(m)
	if len(lines) > m.height-m.popupTop() {
		t.Errorf("the popup is %d lines, past the screen's %d", len(lines), m.height)
	}
	if !strings.Contains(lines[len(lines)-2], "y yes · esc cancel") || !strings.Contains(lines[len(lines)-1], "more") {
		t.Errorf("the keys or what is left went out of sight:\n%s", strings.Join(lines, "\n"))
	}
	press(w, m, "down")
	if !strings.Contains(strings.Join(popupText(m), "\n"), "remove branch-1") || strings.Contains(popupText(m)[1], "branch-0 ") {
		t.Error("down does not scroll the body")
	}
}

func TestThePopupLinesUpOverWideCharacters(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.popup = m.helpPopup()
	ls := m.screen()
	// A line under the popup holds characters two cells wide across its
	// left edge, whichever cell the edge falls on.
	for i := range ls {
		ls[i] = drawn{parts: []cell{{text: strings.Repeat("あ", m.width/2)}}}
	}
	box := m.popupLines()
	left := (m.width - width(box[0].parts)) / 2
	for i, s := range m.overlay(ls) {
		got := ansi.Strip(s)
		if ansi.StringWidth(got) != m.width {
			t.Fatalf("line %d is %d cells wide, not %d", i, ansi.StringWidth(got), m.width)
		}
		if j := i - m.popupTop(); j > 0 && j < len(box)-1 {
			if c := ansi.Cut(got, left, left+1); c != "│" {
				t.Errorf("line %d: the box's edge is %q at cell %d", i, c, left)
			}
		}
	}
}
