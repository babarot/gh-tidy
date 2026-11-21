package tui

import (
	"context"
	"flag"
	"fmt"
	"image/color"
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
	"github.com/babarot/gh-tidy/internal/theme"
	"github.com/babarot/gh-tidy/internal/tui/columns"
	"github.com/babarot/gh-tidy/internal/tui/keymap"
	"github.com/babarot/gh-tidy/internal/tui/statusline"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// Every name, repository and value here is made up.
var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// later is the screen's clock: a minute after the engine's, so that the
// activity box and the rate limit have folded by the time it draws.
var later = now.Add(time.Minute)

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
	lists    map[string]github.Listing
	heavy    map[string]github.HeavyRepo
	details  map[string]model.RepoDetail
	detailed int // details read
	mu       sync.Mutex
	asked    int // repositories asked for by name
}

func (f *fakeGitHub) RepoDetail(_ context.Context, slug string, want github.DetailWant) (model.RepoDetail, github.Rate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detailed++
	d := f.details[strings.ToLower(slug)]
	d.AlertsAsked, d.ChecksAsked, d.TitlesAsked = want.Alerts, want.Oid != "", want.Titles
	d.IssuesRead, d.PRsRead = want.Titles, want.Titles
	return d, github.Rate{Cost: 1}, nil
}

func (f *fakeGitHub) Host() string { return "github.com" }

func (f *fakeGitHub) Viewer(context.Context) (github.Viewer, github.Rate, error) {
	return github.Viewer{Login: "mika", Orgs: []string{"acme"}}, github.Rate{}, nil
}

func (f *fakeGitHub) List(_ context.Context, owner string, _ github.OwnerKind, _ github.Pages, _ int) (github.Listing, error) {
	return f.lists[strings.ToLower(owner)], nil
}

func (f *fakeGitHub) Heavy(_ context.Context, slugs []string, _ github.Want) ([]github.HeavyRepo, github.Rate, error) {
	f.mu.Lock()
	f.asked += len(slugs)
	f.mu.Unlock()
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
	return out, github.Rate{Cost: 1, Date: now}, nil
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
	block chan struct{} // when set, Fetch waits for it
}

func (f *fakeChange) record(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }
func (f *fakeChange) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}
func (f *fakeChange) Fetch(_ context.Context, path string) error {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
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
		gh: &fakeGitHub{lists: map[string]github.Listing{}, heavy: map[string]github.HeavyRepo{}, details: map[string]model.RepoDetail{
			"mika/agent-runner": {Issues: []model.Opened{{Number: 41, Title: "Retry the runner when the socket closes", Author: "kai", Created: now.Add(30 * time.Second)},
				{Number: 37, Title: "Document the cache layout", Author: "ren", Created: ago(2 * 24 * time.Hour)}},
				PRs: []model.Opened{{Number: 40, Title: "Speed up status parsing", Author: "hana", Created: ago(3 * 24 * time.Hour)}}},
			"mika/calendar-sync": {AlertsTotal: 12, Alerts: []model.Alert{
				{Package: "ical-parser", Ecosystem: "npm", Severity: "critical", Range: "< 2.4.1", Fixed: "2.4.1", Manifest: "package-lock.json"},
				{Package: "tmp-dir", Ecosystem: "npm", Severity: "low", Range: "<= 0.9.0", Manifest: "package-lock.json"}},
				Checks: []model.Check{{Name: "test (ubuntu-latest)"}, {Name: "build", Running: true}}, ChecksFailed: 1, ChecksRunning: 1}}}}
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
			Fork: r.fork, Archived: r.archived, Private: r.private, PushedAt: ago(r.pushed), DefaultBranch: "main", Stars: len(r.name) * 7 % 40})
		oid := "oid-" + r.name
		if r.name == "trail-notes" {
			oid = "newer"
		}
		// Every open issue and pull request was made before the first
		// read, but one of agent-runner's issues, made since.
		var issues, prs []time.Time
		for i := range r.issues {
			issues = append(issues, ago(time.Duration(i+2)*24*time.Hour))
		}
		for i := range r.prs {
			prs = append(prs, ago(time.Duration(i+3)*24*time.Hour))
		}
		if r.name == "agent-runner" {
			issues[0] = now.Add(30 * time.Second)
		}
		w.gh.heavy[slug] = github.HeavyRepo{PushedAt: ago(r.pushed), Default: "main", DefaultOid: oid, CI: r.ci,
			Issues: r.issues, PRs: r.prs, Alerts: r.al, AlertsOff: r.name == "weather-cli", ID: int64(len(listed)), IssuesNewest: issues, PRsNewest: prs}
	}
	w.gh.lists["mika"] = github.Listing{Repos: listed, Completeness: github.Complete,
		Pages: github.Pages{ETags: []string{"e1"}, Ended: true, Completeness: github.Complete}}
	w.gh.lists["acme"] = github.Listing{Completeness: github.Complete, Pages: github.Pages{ETags: []string{"e2"}, Ended: true, Completeness: github.Complete}}
	w.change = &fakeChange{local: w.local}
	w.cmds = &fakeCommands{}
	w.eng = engine.New(engine.Options{Roots: []string{root}, Local: w.local, GitHub: w.gh, Change: w.change, Cloner: w.change,
		Now: func() time.Time { return now }})
	if err := w.eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) model(edit func(*Options)) *Model {
	o := Options{Engine: w.eng, Commands: w.cmds, Config: config.Default(), Owners: []string{"acme", "mika"}, Owner: "mika",
		Home: "/src", Now: func() time.Time { return later }, NoTimers: true}
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

// golden compares what m draws, text and looks, with testdata/<name>.golden;
// -update writes it.
func golden(t *testing.T, name string, m *Model) {
	t.Helper()
	got := normalize(t, m.render())
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
	if !sameView(got, string(want)) {
		t.Errorf("%s differs (run with -update to accept):\n%s", name, got)
	}
}

func TestLocal(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	golden(t, "local", m)
	press(w, m, "enter")
	golden(t, "local-worktrees", m)
}

func TestGitHub(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	press(w, m, "v")
	golden(t, "github", m)
}

func TestNoCounts(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) { o.Config.Symbols.Counts = false })
	golden(t, "local-no-counts", m)
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
	// The filter line pushes the list down: a click on the list's first
	// line chooses the first row.
	lines := strings.Split(screen(m), "\n")
	if !strings.HasPrefix(lines[1], " / dot") {
		t.Errorf("the filter is not above the list:\n%s", s)
	}
	m.moveTo(len(m.lines) - 1)
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: m.listTop()})
	if m.sel != 0 {
		t.Errorf("a click on the first line chose line %d", m.sel)
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
	m.unpin()
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
	m.open(m.confirmPopup("clean up", body, func(*Model) tea.Cmd { return nil }))
	lines := popupText(m)
	if len(lines) > m.height-m.popup.top {
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
	m.open(m.helpPopup())
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
		if j := i - m.popup.top; j > 0 && j < len(box)-1 {
			if c := ansi.Cut(got, left, left+1); c != "│" {
				t.Errorf("line %d: the box's edge is %q at cell %d", i, c, left)
			}
		}
	}
}

// scenes are the screens drawn on a light background, and the first on
// every theme.
var scenes = []struct {
	name string
	keys []string
}{
	{"local", nil},
	{"local-worktrees", []string{"enter"}},
	{"github", []string{"v"}},
	{"confirm", []string{"P"}},
}

var white = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

func TestLightBackground(t *testing.T) {
	for _, sc := range scenes {
		t.Run(sc.name, func(t *testing.T) {
			w := newWorld(t)
			m := w.model(nil)
			m.Update(tea.BackgroundColorMsg{Color: white})
			press(w, m, sc.keys...)
			golden(t, "light-"+sc.name, m)
		})
	}
}

// TestThemes draws one screen on every theme, so that a theme added gets
// a golden file without a test of its own.
func TestThemes(t *testing.T) {
	for _, name := range theme.Names() {
		if name == theme.Auto {
			continue
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			m := w.model(func(o *Options) { o.Config.TUI.Theme = name })
			golden(t, "theme-"+name, m)
		})
	}
}

// Every popup reads the keys the same way: nav moves, esc steps back one
// level, the key that opened it does not close it, and quit is not read.
func TestPopupsReadKeysAlike(t *testing.T) {
	for _, tc := range []struct {
		name   string
		open   func(*Model) *popup
		reopen string // the key that opens it
	}{
		{"owner", (*Model).ownerPopup, "O"},
		{"help", (*Model).helpPopup, "?"},
		{"confirm", func(m *Model) *popup {
			var body []drawn
			for i := range 60 {
				body = append(body, drawn{parts: []cell{{text: fmt.Sprintf("line %d", i)}}})
			}
			return m.confirmPopup("clean up", body, func(*Model) tea.Cmd { return nil })
		}, "P"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			m := w.model(nil)
			m.open(tc.open(m))
			p := m.popup
			if p.list != nil {
				p.list.sel = 0 // the owner shown may be the last
			}
			pos := func() int {
				if p.list != nil {
					return p.list.sel
				}
				return p.body.top
			}
			before := pos()
			press(w, m, "down")
			if pos() == before {
				t.Error("down did not move")
			}
			press(w, m, tc.reopen)
			if m.popup == nil {
				t.Errorf("%s closed the popup it opened", tc.reopen)
			}
			press(w, m, "q")
			if m.popup == nil {
				t.Error("q closed the popup")
			}
			press(w, m, "esc")
			if m.popup != nil && p.list != nil && p.list.query != "" {
				t.Error("esc did not clear what was typed")
			}
			press(w, m, "esc")
			if m.popup != nil {
				t.Error("esc did not close the popup")
			}
		})
	}
}

// The popup's hints come from the keymap, and the key line under it says
// only what writes, and esc.
func TestPopupHintsFollowTheKeys(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) { o.Config.Keys = map[string]any{"confirm": "x", "choose": "tab"} })
	m.open(m.confirmPopup("clean up", []drawn{{parts: []cell{{text: "remove a"}}}}, func(*Model) tea.Cmd { return nil }))
	if s := strings.Join(popupText(m), "\n"); !strings.Contains(s, "x yes · esc cancel") {
		t.Errorf("confirm foot:\n%s", s)
	}
	if line := plain(m.hintLine(0)); !strings.Contains(line, "x yes") || !strings.Contains(line, "esc cancel") {
		t.Errorf("key line under the confirm: %q", line)
	}
	m.open(m.ownerPopup())
	if s := strings.Join(popupText(m), "\n"); !strings.Contains(s, "tab choose") {
		t.Errorf("owner foot:\n%s", s)
	}
	if line := plain(m.hintLine(0)); strings.Contains(line, "choose") || !strings.Contains(line, "esc close") {
		t.Errorf("key line under the owner popup says what does not write: %q", line)
	}
}

// One click puts the cursor on an item; a click on the item the cursor is
// on chooses it. A click on what a confirm says does nothing; a click
// outside closes.
func TestPopupMouse(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.open(m.ownerPopup())
	m.render()
	p := m.popup
	row := -1
	for i, it := range p.lines {
		if it >= 0 && it != p.list.sel {
			row = i
			break
		}
	}
	click := func(x, y int) { m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y}); m.render() }
	click(p.x0+4, p.top+row)
	if m.popup == nil || p.list.sel != p.lines[row] {
		t.Fatalf("the first click chose, or did not move: popup %v", m.popup != nil)
	}
	chosen := p.list.shown()[p.list.sel]
	click(p.x0+4, p.top+row)
	if m.popup != nil || m.owner != chosen {
		t.Errorf("the second click did not choose %s (owner %s)", chosen, m.owner)
	}

	ran := false
	m.open(m.confirmPopup("clean up", []drawn{{parts: []cell{{text: "remove a"}}}}, func(*Model) tea.Cmd { ran = true; return nil }))
	m.render()
	for y := m.popup.top; y < m.popup.top+len(m.popup.lines); y++ {
		click(m.popup.x0+4, y)
	}
	if ran || m.popup == nil {
		t.Errorf("a click inside the confirm ran it (%v) or closed it", ran)
	}
	click(0, 0)
	if m.popup != nil {
		t.Error("a click outside did not close the popup")
	}
}

// A popup opens a quarter down the screen, or in the middle when it is too
// tall for that, and stays where it opened while what it holds changes.
func TestPopupStandsWhereItOpened(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.open(m.ownerPopup())
	if m.popup.top != m.height/4-2 {
		t.Errorf("a short popup at line %d", m.popup.top)
	}
	top := m.popup.top
	press(w, m, "z")
	if m.popup.top != top {
		t.Error("the popup moved as its list narrowed")
	}
	var body []drawn
	for range 100 {
		body = append(body, drawn{})
	}
	m.open(m.confirmPopup("clean up", body, func(*Model) tea.Cmd { return nil }))
	if m.popup.top >= m.height/4-2 {
		t.Errorf("a tall popup at line %d, not higher", m.popup.top)
	}
}

// Scrolling a long confirm keeps every line of what it asks about.
func TestScrollingAConfirmKeepsItsLines(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	var body []drawn
	for i := range 100 {
		body = append(body, drawn{parts: []cell{{text: fmt.Sprintf("remove branch-%d", i)}}})
	}
	m.open(m.confirmPopup("clean up", body, func(*Model) tea.Cmd { return nil }))
	for range 40 {
		press(w, m, "down")
		m.render()
	}
	for i, l := range m.popup.body.lines {
		if got, want := plain(l), fmt.Sprintf("remove branch-%d", i); got != want {
			t.Fatalf("line %d is %q, not %q", i, got, want)
		}
	}
}

// In a list longer than the popup, the item under the mouse stays under it
// after the first click, so the second click chooses it.
func TestClickingALongList(t *testing.T) {
	w := newWorld(t)
	owners := []string{"acme", "mika"}
	for i := range 60 {
		owners = append(owners, fmt.Sprintf("owner-%02d", i))
	}
	m := w.model(func(o *Options) { o.Owners, o.Owner = owners, "owner-59" })
	m.open(m.ownerPopup())
	m.render()
	p := m.popup
	row := -1
	for i, it := range p.lines {
		if it >= 0 && it != p.list.sel {
			row = i
			break
		}
	}
	want := p.list.shown()[p.lines[row]]
	for range 2 {
		m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: p.x0 + 4, Y: p.top + row})
		m.render()
	}
	if m.popup != nil || m.owner != want {
		t.Errorf("chose %s, not %s, the item clicked", m.owner, want)
	}
}

// A list narrowed to nothing takes the keys that move and choose.
func TestAnEmptyListTakesKeys(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.open(m.ownerPopup())
	for _, k := range []string{"z", "z", "z", "down", "G", "enter"} {
		m.popupKey(k)
	}
	if m.popup == nil {
		t.Error("enter on nothing closed the popup")
	}
}

func TestRefreshKey(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	w.gh.mu.Lock()
	before := w.gh.asked
	w.gh.mu.Unlock()
	press(w, m, "r")
	w.gh.mu.Lock()
	defer w.gh.mu.Unlock()
	if w.gh.asked == before {
		t.Error("r did not read GitHub's fields again")
	}
}

// While work goes on, the activity box shows the run's counts and its
// tasks, and the status line says nothing of it; once the run is over the
// box folds.
func TestTheActivityBoxShowsTheWork(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	if s := screen(m); strings.Contains(s, "[+]") {
		t.Errorf("a box with no work going on:\n%s", s)
	}
	block := make(chan struct{})
	w.change.mu.Lock()
	w.change.block = block
	w.change.mu.Unlock()
	m.Update(tea.KeyPressMsg{Code: 'F', Text: "F"})
	// The fetches wait on block once the net workers take them.
	for deadline := time.Now().Add(5 * time.Second); len(m.eng.Activity().Running) < 4; {
		if time.Now().After(deadline) {
			t.Fatalf("the fetches did not start: %+v", m.eng.Activity())
		}
		time.Sleep(time.Millisecond)
	}
	s := screen(m)
	if !strings.Contains(s, "[+] Fetching mika 0/6 fetched") || !strings.Contains(s, "fetching") || !strings.Contains(s, "+1 running") {
		t.Errorf("the box while fetching:\n%s", s)
	}
	if status := strings.TrimSpace(plain(m.statusLine(m.eng.Activity(), false))); status != "" {
		t.Errorf("status line beside the box: %q", status)
	}
	m.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	if s := screen(m); !strings.Contains(s, "workers  disk") || !strings.Contains(s, "net 4/4") {
		t.Errorf("w did not show the workers:\n%s", s)
	}
	close(block)
	w.settle(m)
	if s := screen(m); strings.Contains(s, "[+]") || strings.Contains(s, "workers  disk") {
		t.Errorf("the box did not fold:\n%s", s)
	}
}

// fixedActivity is an engine whose run is always a, so that a screen of
// a run does not hang on the machine: its number of CPUs, or which tasks
// its workers took first.
type fixedActivity struct {
	Engine
	a model.Activity
}

func (f fixedActivity) Activity() model.Activity { return f.a }

// The box of a run, with the rate limit in its head and the workers
// under it, as a golden.
func TestTheActivityBoxLooks(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	task := func(kind, name, note string, failed bool, took time.Duration) model.Task {
		x := model.Task{Kind: kind, Name: name, Note: note, Failed: failed, Start: later.Add(-took - time.Second)}
		if note != "" || failed {
			x.End = later.Add(-time.Second)
		}
		return x
	}
	m.eng = fixedActivity{Engine: m.eng, a: model.Activity{Verb: "Reading", What: "mika", Started: later.Add(-4 * time.Second),
		Counts:  []model.Count{{Name: "clones", Done: 5, Total: 7}, {Name: "GitHub", Done: 1, Total: 3}},
		Running: []model.Task{task("clone", "trail-notes", "", false, 0), task("github", "fields · 2 repositories", "", false, 0)},
		Done:    []model.Task{task("clone", "scratchpad", "failed", true, time.Second), task("clone", "dotfiles", "read", false, 2*time.Second)},
		Workers: []model.Workers{{Name: "disk", Running: 1, Limit: 8}, {Name: "net", Running: 0, Limit: 4}, {Name: "api", Running: 2, Limit: 2}},
		Queued:  3,
		Rate:    model.RateLeft{Known: true, Remaining: 4952, Limit: 5000, Used: 12, Saved: 3, Asking: true}}}
	m.workers = true
	golden(t, "activity", m)
}

// The rate limit shows while gh tidy asks GitHub's API and for a while
// after: in the box's head when it fits, on the status line when not.
func TestTheRateLimit(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	a := model.Activity{Verb: "Reading", What: "mika", Started: now, Counts: []model.Count{{Name: "GitHub", Done: 1, Total: 3}},
		Rate: model.RateLeft{Known: true, Remaining: 4952, Limit: 5000, Used: 12, Saved: 3, Asking: true}}
	box, rateIn := m.activityBox(a)
	if !rateIn || !strings.Contains(plain(box[0]), "rate limit 4,952/5,000  │ this load 12 used · 3 saved") {
		t.Errorf("head: %q", plain(box[0]))
	}
	if s := plain(m.statusLine(a, rateIn)); strings.Contains(s, "rate limit") {
		t.Errorf("the rate limit twice: %q", s)
	}
	m.width = 60
	box, rateIn = m.activityBox(a)
	if rateIn || strings.Contains(plain(box[0]), "rate limit") {
		t.Errorf("a narrow head kept the rate limit: %q", plain(box[0]))
	}
	if s := plain(m.statusLine(a, rateIn)); !strings.Contains(s, "rate limit 4,952/5,000") {
		t.Errorf("status line: %q", s)
	}
	m.width = 140
	a.Rate.Asking, a.Rate.LastAsked = false, m.now().Add(-rateFor)
	if m.rateShown(a) {
		t.Error("the rate limit stayed after the last request")
	}
	a.Rate.Remaining = 400
	if got := m.rate(a, true)[1].role; got != roleWarn {
		t.Errorf("under a tenth left: role %v", got)
	}
}

// A click on a key in the key line presses it, unless the key changes a
// clone or asks GitHub: the mouse only moves, opens and folds. A click
// that switches the columns may read what they show, as a key would.
func TestClicksOnTheKeyLineChangeNothing(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	for _, lens := range columns.Lenses {
		m.setLens(lens)
		m.screen()
		y := m.height - 1
		var xs []int
		for _, h := range m.hits {
			if h.y == y {
				xs = append(xs, h.x0)
			}
		}
		if len(xs) == 0 {
			t.Fatalf("%s: no key on the key line takes a click", lens)
		}
		for x := range m.width {
			_, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			w.run(m, cmd)
			w.settle(m)
			if m.popup != nil && m.popup.question != nil {
				t.Errorf("%s: a click at %d asks to change something", lens, x)
			}
			m.popup = nil
		}
	}
	if calls := w.change.seen(); len(calls) > 0 {
		t.Errorf("clicks changed clones: %q", calls)
	}
}

// docs/keys.md is the list of keys ? shows, with the default keys;
// go test ./internal/tui -run TestKeysReference -update writes it.
func TestKeysReference(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "keys.md")
	want := keysReference()
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Error("docs/keys.md is not the list of keys; run go test ./internal/tui -run TestKeysReference -update")
	}
}

// ? lists every action with a key, in every scope, the actions from the
// config file, and the mouse; a key set under [keys] shows as set.
func TestHelpListsEveryKey(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) {
		o.Config.Keys = map[string]any{"fetch": "x"}
		o.Config.Actions = []config.Action{{Key: "H", Name: "open in herdr", Scope: config.ScopeRepo, Run: []string{"true"}}}
	})
	var text []string
	for _, l := range m.helpPopup().body.lines {
		text = append(text, plain(l))
	}
	all := strings.Join(text, "\n")
	for _, d := range keymap.Defaults {
		if !strings.Contains(all, string(d.Action)) && !strings.Contains(all, d.Help) {
			t.Errorf("? leaves out %s", d.Action)
		}
	}
	var words []string
	for _, l := range text {
		words = append(words, strings.Join(strings.Fields(l), " "))
	}
	for _, want := range []string{"x fetch", "H open in herdr · runs on the repo", "A popup that asks before a change", "double click", "Typing the filter"} {
		if !slices.ContainsFunc(words, func(l string) bool { return strings.HasPrefix(l, want) }) {
			t.Errorf("? lacks %q:\n%s", want, all)
		}
	}
}

// Dragging the pane's top line changes its height, within its limits, and
// keeps it when the button is let go, as + and - do.
func TestDraggingThePaneTop(t *testing.T) {
	w := newWorld(t)
	var saved []int
	m := w.model(func(o *Options) { o.Save = func(_, _ string, ph int) { saved = append(saved, ph) } })
	m.screen()
	before := m.ph
	top := m.listTop() + m.listHeight() + 1
	if !strings.HasPrefix(plain(m.screen()[top]), "╭") {
		t.Fatalf("line %d is not the pane's top: %q", top, plain(m.screen()[top]))
	}
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: top})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 10, Y: top - 2})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 10, Y: top - 3})
	if m.ph != before+3 || len(saved) != 0 {
		t.Errorf("while dragging: height %d, want %d; saved %v", m.ph, before+3, saved)
	}
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 10, Y: top - 3})
	if !slices.Equal(saved, []int{before + 3}) {
		t.Errorf("saved %v; want the height once, on release", saved)
	}
	if !strings.HasPrefix(plain(m.screen()[top-3]), "╭") {
		t.Errorf("the pane's top is not where it was let go")
	}
	// Far down, it stops at its least; a move with no button held does
	// nothing.
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: top - 3})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 10, Y: m.height})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 10, Y: m.height})
	if m.ph != previewMin {
		t.Errorf("dragged to the bottom: height %d, want %d", m.ph, previewMin)
	}
	m.Update(tea.MouseMotionMsg{X: 10, Y: 2})
	if m.ph != previewMin {
		t.Errorf("a move with no button changed the height to %d", m.ph)
	}
}

// The github pane lists the selected repository's alerts and checks, read
// for it: the most severe first, the rest counted.
func TestTheGitHubPaneListsAlertsAndChecks(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.setLens(columns.GitHub)
	w.settle(m)
	for i, l := range m.lines {
		if l.row != nil && l.row.Remote == "github.com/mika/calendar-sync" {
			m.moveTo(i)
		}
	}
	m.sync()
	w.settle(m)
	s := screen(m)
	for _, want := range []string{"critical  ical-parser (npm)  < 2.4.1   fixed in 2.4.1  package-lock.json", "low       tmp-dir (npm)", "no fix yet",
		"no fix yet      package-lock.json", "… 10 more", "✗ test (ubuntu-latest)"} {
		if !strings.Contains(s, want) {
			t.Errorf("the pane lacks %q:\n%s", want, s)
		}
	}
	golden(t, "github-detail", m)
	press(w, m, "J", "J")
	if s := screen(m); !strings.Contains(s, "~ build") {
		t.Errorf("the running check is not listed:\n%s", s)
	}
}

// With the timers on, a row's GitHub detail is read only once the cursor
// rests on it: moving on before the wait ends reads nothing.
func TestTheDetailWaitsForTheCursorToRest(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.setLens(columns.GitHub)
	w.settle(m)
	m.noTimers = false
	w.gh.mu.Lock()
	before := w.gh.detailed
	w.gh.mu.Unlock()
	row := func(name string) int {
		for i, l := range m.lines {
			if l.row != nil && l.row.Remote == model.RemoteKey("github.com/mika/"+name) {
				return i
			}
		}
		t.Fatalf("no row %s", name)
		return 0
	}
	m.moveTo(row("tool-shed")) // alerts, so its detail is wanted
	m.sync()
	if m.settle() == nil || m.settle() != nil {
		t.Fatal("the wait did not start once")
	}
	m.moveTo(row("calendar-sync"))
	m.sync()
	m.Update(detailMsg{k: "github.com/mika/tool-shed"}) // its wait ends elsewhere
	w.settle(m)
	w.gh.mu.Lock()
	n := w.gh.detailed - before
	w.gh.mu.Unlock()
	if n != 0 {
		t.Fatalf("read %d details for rows only passed", n)
	}
	m.Update(detailMsg{k: "github.com/mika/calendar-sync"})
	w.settle(m)
	if m.ghd.State != model.DetailRead || len(m.ghd.Value.Alerts) == 0 {
		t.Errorf("after resting: %+v", m.ghd)
	}
}

// A new issue shows as +1 in ISSUES, and its title is marked in the pane;
// once the cursor rested on the row and left, it is seen.
func TestNewIssuesAreMarkedUntilSeen(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.setLens(columns.GitHub)
	w.settle(m)
	i := -1
	for j, l := range m.lines {
		if l.row != nil && l.row.Remote == "github.com/mika/agent-runner" {
			i = j
		}
	}
	cols, ws := m.layout()
	if s := plain(m.row(m.lines[i], false, cols, ws)); !strings.Contains(s, "4 +1") {
		t.Errorf("agent-runner's row: %q", s)
	}
	m.moveTo(i)
	m.sync()
	w.settle(m)
	s := screen(m)
	if !strings.Contains(s, "• issue #41 Retry the runner when the socket closes") || strings.Contains(s, "• issue #37") {
		t.Errorf("the pane does not mark the new issue alone:\n%s", s)
	}
	golden(t, "github-new", m)
	m.moveTo(0)
	m.sync()
	w.settle(m)
	if s := plain(m.row(m.lines[i], false, cols, ws)); strings.Contains(s, "+1") {
		t.Errorf("after the cursor left: %q", s)
	}
}

// With the timers on, resting on a row whose titles are already held
// still marks them seen once the rest is over: no read is needed for it.
func TestRestingOnHeldTitlesMarksThemSeen(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.setLens(columns.GitHub)
	w.settle(m)
	i := -1
	for j, l := range m.lines {
		if l.row != nil && l.row.Remote == "github.com/mika/agent-runner" {
			i = j
		}
	}
	m.moveTo(i)
	m.sync()
	w.settle(m) // read and held, the timers off
	m.moveTo(0)
	m.sync()
	w.settle(m)
	cols, ws := m.layout()
	if s := plain(m.row(m.lines[i], false, cols, ws)); strings.Contains(s, "+1") {
		t.Fatalf("seen while the timers were off: %q", s)
	}
	m.noTimers = false
	m.ghdRested = ""
	// Back on the row: the titles are held, and passing marks nothing.
	m.moveTo(i)
	m.sync()
	if m.ghd.State != model.DetailRead || m.settle() == nil {
		t.Fatalf("no rest started over held titles: %+v", m.ghd.State)
	}
	m.Update(detailMsg{k: "github.com/mika/agent-runner"})
	w.settle(m)
	m.moveTo(0)
	m.sync()
	w.settle(m)
	if s := plain(m.row(m.lines[i], false, cols, ws)); strings.Contains(s, "+1") {
		t.Errorf("the held titles were not marked seen: %q", s)
	}
}

// The sections the user names gather every row they match, attention or
// not, cloned or not, in the first one matched; a section with nothing in
// it is left out, and folded = false starts open.
func TestSectionsTheUserNames(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) {
		o.Config.Groups = []config.Group{
			{Name: "calendar work", Repos: []string{"calendar-*", "MIKA/tool-*"}},
			{Name: "notes", Repos: []string{"trail-notes", "calendar-sync"}, Folded: true},
			{Name: "nobody", Repos: []string{"other/*"}},
		}
	})
	s := screen(m)
	for _, want := range []string{"▾ calendar work  2", "▸ notes  1"} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "nobody") {
		t.Errorf("an empty section is shown:\n%s", s)
	}
	// calendar-sync needs attention, yet it is in its section, the first
	// matched; tool-shed is not cloned, matched by owner/name.
	var head string
	where := map[string]string{}
	for _, l := range m.lines {
		switch {
		case l.kind == columns.KindHead:
			head = l.title
		case l.row != nil:
			where[l.name()] = head
		}
	}
	if where["calendar-sync"] != "calendar work" {
		t.Errorf("calendar-sync under %q", where["calendar-sync"])
	}
	if _, ok := where["trail-notes"]; ok {
		t.Error("a row of a folded section is shown")
	}
	press(w, m, "/", "t", "r", "a", "i", "l", "enter")
	if s := screen(m); !strings.Contains(s, "trail-notes") {
		t.Errorf("the filter does not open the folded section:\n%s", s)
	}
	press(w, m, "esc")
	m.setLens(columns.GitHub)
	w.settle(m)
	head, where = "", map[string]string{}
	for _, l := range m.lines {
		switch {
		case l.kind == columns.KindHead:
			head = l.title
		case l.row != nil:
			where[l.name()] = head
		}
	}
	if where["tool-shed"] != "calendar work" || where["calendar-sync"] != "calendar work" {
		t.Errorf("in the github columns: %v", where)
	}
	golden(t, "sections-github", m)
}

// While GitHub is read, the rows of a section keep their places, as those
// above the heads do; once it is done they take their order.
func TestRowsInASectionKeepTheirPlaces(t *testing.T) {
	w := newWorld(t)
	m := w.model(func(o *Options) {
		o.Config.Groups = []config.Group{{Name: "work", Repos: []string{"calendar-sync", "tool-shed"}}}
	})
	m.setLens(columns.GitHub)
	w.settle(m)
	names := func() []string {
		var out []string
		in := false
		for _, l := range m.lines {
			if l.kind == columns.KindHead {
				in = l.title == "work"
				continue
			}
			if in {
				out = append(out, l.name())
			}
		}
		return out
	}
	before := names()
	if len(before) != 2 || before[0] != "calendar-sync" {
		t.Fatalf("work = %q", before)
	}
	m.snap.Reading = "reading GitHub"
	m.relayout() // the order is taken
	for i := range m.snap.Rows {
		if m.snap.Rows[i].Remote == "github.com/mika/tool-shed" {
			m.snap.Rows[i].Heavy.Value.Alerts = 900 // now it ranks first
		}
	}
	m.relayout()
	if got := names(); !slices.Equal(got, before) {
		t.Errorf("while reading: %q; want %q kept", got, before)
	}
	m.snap.Reading = ""
	m.relayout()
	if got := names(); got[0] != "tool-shed" {
		t.Errorf("after reading: %q", got)
	}
}

// Switching the columns there and back while rows keep their places
// leaves nothing to do as it was: which rows were above the first head is
// let go with their order.
func TestTheQuietHeadStaysAcrossALensSwitch(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	m.snap.Reading = "reading GitHub"
	m.relayout()
	if !strings.Contains(screen(m), "nothing to do  2") {
		t.Fatalf("before:\n%s", screen(m))
	}
	m.setLens(columns.GitHub)
	m.snap.Reading = "reading GitHub"
	m.relayout()
	m.setLens(columns.Local)
	m.snap.Reading = "reading GitHub"
	m.relayout()
	if s := screen(m); !strings.Contains(s, "nothing to do  2") {
		t.Errorf("after switching there and back:\n%s", s)
	}
}

// The box is as tall as what it shows, and takes the bottom of the
// preview, which closes above it: the list keeps its height and the
// preview its top.
func TestTheActivityBoxTakesThePreviewsBottom(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	before := m.screen()
	height := m.listHeight()
	a := model.Activity{Verb: "Reading", What: "mika", Started: later.Add(-time.Second), Counts: []model.Count{{Name: "GitHub", Done: 1, Total: 1}},
		Done: []model.Task{{Kind: "github", Name: "list mika", Note: "304", Start: later.Add(-time.Second), End: later}}}
	box, _ := m.activityBox(a)
	if len(box) != 2 {
		t.Fatalf("a box with one task has %d lines; want the head and the task", len(box))
	}
	m.eng = fixedActivity{Engine: m.eng, a: a}
	after := m.screen()
	if m.listHeight() != height || len(after) != len(before) {
		t.Errorf("the list went from %d lines to %d, the screen from %d to %d", height, m.listHeight(), len(before), len(after))
	}
	status := len(after) - 2
	if got := plain(after[status-2]); !strings.Contains(got, "[+] Reading mika") {
		t.Errorf("the box's head is not at the preview's bottom: %q", got)
	}
	if got := plain(after[status-3]); !strings.HasPrefix(got, "╰") {
		t.Errorf("the preview does not close above the box: %q", got)
	}
	top := m.listTop() + height + 1
	// The top is where it was; only how many lines are below the fold,
	// which it counts, may differ.
	title := func(d drawn) string { return strings.Fields(plain(d))[0] + " " + strings.Fields(plain(d))[1] }
	if !strings.HasPrefix(plain(after[top]), "╭") || title(after[top]) != title(before[top]) {
		t.Errorf("the preview's top moved: %q, was %q", plain(after[top]), plain(before[top]))
	}
	// A low preview keeps its two borders; the box keeps its head and its
	// newest tasks.
	tasks := []model.Task{{Kind: "clone", Name: "a"}, {Kind: "clone", Name: "b"}, {Kind: "clone", Name: "c"}}
	big, _ := m.activityBox(model.Activity{Verb: "Reading", What: "mika", Started: later.Add(-time.Second), Running: tasks})
	if got := fitBox(big, 3); len(got) != 3 || !strings.Contains(plain(got[0]), "[+]") || !strings.Contains(plain(got[2]), " c ") {
		t.Errorf("fitBox = %q", func() []string {
			var out []string
			for _, d := range got {
				out = append(out, plain(d))
			}
			return out
		}())
	}
}

// While a clone is fetched, a light runs over what MAIN says: its colors
// change from frame to frame, never its text.
func TestTheShimmerMovesAndKeepsTheText(t *testing.T) {
	w := newWorld(t)
	m := w.model(nil)
	cs := []cell{{text: "fetching", role: roleAccent}}
	plainOf := func(cs []cell) string { return plain(drawn{parts: cs}) }
	first := m.shimmer(cs)
	m.now = func() time.Time { return later.Add(3 * statusline.FrameEvery) }
	second := m.shimmer(cs)
	if plainOf(first) != "fetching" || plainOf(second) != "fetching" {
		t.Fatalf("the text changed: %q, %q", plainOf(first), plainOf(second))
	}
	roles := func(cs []cell) []columns.Role {
		var out []columns.Role
		for _, c := range cs {
			for range []rune(c.text) {
				out = append(out, c.role)
			}
		}
		return out
	}
	if slices.Equal(roles(first), roles(second)) {
		t.Error("the light did not move")
	}
	if !slices.Contains(roles(second), roleStrong) || !slices.Contains(roles(second), roleAccent) {
		t.Errorf("roles = %v; want the light and the text's own color", roles(second))
	}
}
