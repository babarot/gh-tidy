package tui

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/gittest"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
)

// world is a root with one owner, "me", whose clones are made from bare
// remotes in the same temporary directory.
type world struct {
	t       *testing.T
	base    string
	root    string
	owner   string
	remotes map[string]string
}

func newWorld(t *testing.T) *world {
	gittest.Isolate(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{t: t, base: base, root: filepath.Join(base, "src"), remotes: map[string]string{}}
	w.owner = filepath.Join(w.root, "github.com", "me")
	os.MkdirAll(w.owner, 0o755)
	return w
}

// clone makes a remote called name and clones it under the owner.
func (w *world) clone(name string) string {
	w.remotes[name] = gittest.Remote(w.t, w.base, name)
	dir := filepath.Join(w.owner, name)
	gittest.Clone(w.t, w.remotes[name], dir, "me/"+name)
	return dir
}

// advance pushes n commits to the remote's main from elsewhere.
func (w *world) advance(name string, n int) {
	other := filepath.Join(w.base, "pusher-"+name)
	if _, err := os.Stat(other); err != nil {
		gittest.Git(w.t, w.base, "clone", "-q", w.remotes[name], other)
	}
	gittest.Git(w.t, other, "pull", "-q")
	for i := range n {
		gittest.Commit(w.t, other, "f"+name, strings.Repeat("x", i+1)+time.Now().String())
	}
	gittest.Git(w.t, other, "push", "-q", "origin", "main")
}

func (w *world) model(cfgEdit func(*config.File)) *Model {
	cfg := config.Default()
	cfg.Core.StaleCheck = false // no GitHub in tests
	cfg.TUI.Hide = nil
	if cfgEdit != nil {
		cfgEdit(&cfg)
	}
	m, err := New(context.Background(), cfg, w.root, locate.Target{Kind: locate.Owner, Host: "github.com", Owner: "me"})
	if err != nil {
		w.t.Fatal(err)
	}
	m.noTimers = true
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	drive(w.t, m, m.Init())
	return m
}

// drive runs cmd and feeds what it returns back into m until nothing is
// left. The model's timers are off, so every command returns.
func drive(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- c() }()
		var msg tea.Msg
		select {
		case msg = <-ch:
		case <-time.After(30 * time.Second):
			t.Fatal("a command did not return")
		}
		switch msg := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tickMsg, statusExpMsg:
		default:
			if reflect.TypeOf(msg).PkgPath() != "github.com/babarot/gh-fleet/internal/tui" {
				continue // clipboard, background color requests
			}
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

func press(t *testing.T, m *Model, key string) {
	t.Helper()
	var k tea.KeyPressMsg
	switch key {
	case "enter":
		k = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		k = tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		k = tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
	}
	_, cmd := m.Update(k)
	drive(t, m, cmd)
}

func (w *world) find(m *Model, name string) *repo.Repo {
	for _, r := range m.repos {
		if r.Name == name {
			return r
		}
	}
	w.t.Fatalf("%s not listed", name)
	return nil
}

func plain(m *Model) string { return ansi.Strip(m.render()) }

// selectRepo moves the selection to name.
func selectRepo(t *testing.T, m *Model, name string) {
	t.Helper()
	for i, r := range m.rows() {
		if r.wt == nil && r.r.Name == name {
			m.sel = i
			return
		}
	}
	t.Fatalf("%s is not a row", name)
}

func TestListShowsState(t *testing.T) {
	w := newWorld(t)
	app := w.clone("app")
	w.clone("quiet")
	w.advance("app", 2)
	gittest.Git(t, app, "fetch", "-q")
	os.WriteFile(filepath.Join(app, "README"), []byte("edit\n"), 0o644)

	m := w.model(func(c *config.File) { c.TUI.Hide = []string{config.HideQuiet} })
	s := plain(m)
	for _, want := range []string{"cloned 2", "app", "1*", "<2", "1 repositories · 1 with nothing to say hidden (a)"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "quiet") {
		t.Errorf("a quiet repository is listed while hidden:\n%s", s)
	}
	press(t, m, "a")
	if !strings.Contains(plain(m), "quiet") {
		t.Errorf("a shows the hidden repository")
	}
}

func TestFetchAndPull(t *testing.T) {
	w := newWorld(t)
	w.clone("app")
	m := w.model(nil)
	w.advance("app", 3)

	selectRepo(t, m, "app")
	press(t, m, "f")
	if r := w.find(m, "app"); r.Behind != 3 {
		t.Fatalf("after f: behind %d, want 3", r.Behind)
	}
	press(t, m, "p")
	if r := w.find(m, "app"); r.Behind != 0 {
		t.Errorf("after p: behind %d, want 0", r.Behind)
	}
	if !strings.Contains(plain(m), "pulled app") {
		t.Errorf("no status after pull:\n%s", plain(m))
	}
}

func TestPullRefusesDirty(t *testing.T) {
	w := newWorld(t)
	app := w.clone("app")
	w.advance("app", 1)
	gittest.Git(t, app, "fetch", "-q")
	os.WriteFile(filepath.Join(app, "README"), []byte("edit\n"), 0o644)
	m := w.model(nil)
	selectRepo(t, m, "app")
	press(t, m, "p")
	if r := w.find(m, "app"); r.Behind != 1 {
		t.Errorf("a dirty clone was pulled")
	}
	if !strings.Contains(plain(m), "not pulled, 1 uncommitted changes") {
		t.Errorf("no reason given:\n%s", plain(m))
	}
}

func TestPullAll(t *testing.T) {
	w := newWorld(t)
	w.clone("behind")
	dirty := w.clone("dirty")
	ahead := w.clone("ahead")
	m := w.model(nil)
	w.advance("behind", 2)
	w.advance("dirty", 1)
	os.WriteFile(filepath.Join(dirty, "README"), []byte("edit\n"), 0o644)
	gittest.Commit(t, ahead, "mine", "mine")
	w.advance("ahead", 1) // diverged once fetched

	press(t, m, "P")
	if r := w.find(m, "behind"); r.Behind != 0 {
		t.Errorf("behind: still behind %d", r.Behind)
	}
	if r := w.find(m, "dirty"); r.Behind != 1 {
		t.Errorf("dirty: behind %d, want 1 (fetched, not pulled)", r.Behind)
	}
	if r := w.find(m, "ahead"); r.Ahead != 1 || r.Behind != 1 {
		t.Errorf("ahead: %d/%d, want diverged 1/1", r.Ahead, r.Behind)
	}
	if !strings.Contains(plain(m), "fetched all; pulled 1") {
		t.Errorf("no summary:\n%s", plain(m))
	}
}

func TestExpandWorktrees(t *testing.T) {
	w := newWorld(t)
	app := w.clone("app")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "feature", filepath.Join(w.base, "wt", "feature"))
	m := w.model(nil)
	selectRepo(t, m, "app")
	press(t, m, "enter")
	s := plain(m)
	if !strings.Contains(s, "└─ ⌥ feature") || !strings.Contains(s, "├─ ⌥ (main)") {
		t.Errorf("worktrees not expanded:\n%s", s)
	}
	m.sel++ // onto the main worktree row
	press(t, m, "enter")
	if strings.Contains(plain(m), "└─ ⌥ feature") {
		t.Errorf("enter on a worktree row does not fold")
	}
}

func TestKeysFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Keys = map[string]any{"fetch": "x", "quit": []any{"Q"}}
	cfg.Actions = []config.Action{{Key: "H", Name: "herdr", Run: []string{"true"}}}
	km, err := newKeymap(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"x": opFetch, "f": "", "Q": opQuit, "q": "", "esc": "", "H": "action:0", "p": opPull} {
		if got := km.op(key); got != want {
			t.Errorf("key %q = %q, want %q", key, got, want)
		}
	}
	cfg.Keys = map[string]any{"fetch": "p", "pull": "p"}
	if _, err := newKeymap(cfg); err == nil || !strings.Contains(err.Error(), `both use "p"`) {
		t.Errorf("a shared key = %v", err)
	}
	cfg.Keys = map[string]any{"fetchh": "x"}
	if _, err := newKeymap(cfg); err == nil || !strings.Contains(err.Error(), "fetchh is not an operation") {
		t.Errorf("an unknown operation = %v", err)
	}
}

func TestCleanupDialog(t *testing.T) {
	w := newWorld(t)
	app := w.clone("app")
	wt := filepath.Join(w.base, "wt", "landed")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "landed", wt)
	gittest.Commit(t, wt, "l", "landed work")
	gittest.Git(t, wt, "push", "-q", "origin", "landed:main")
	gittest.Git(t, app, "fetch", "-q")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "fresh", filepath.Join(w.base, "wt", "fresh"), "origin/main")

	m := w.model(nil)
	selectRepo(t, m, "app")
	press(t, m, "d")
	s := plain(m)
	for _, want := range []string{"clean up app", "REMOVE 1", "⌥ landed landed", "KEEP 1", "⌥ fresh fresh  no commits since it was created"} {
		if !strings.Contains(s, want) {
			t.Errorf("dialog lacks %q:\n%s", want, s)
		}
	}
	press(t, m, "y") // copy path outside the dialog; nothing here
	press(t, m, "j")
	if m.confirm == nil {
		t.Fatal("a key other than enter or esc closed the dialog")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("removed before enter")
	}
	press(t, m, "enter")
	if m.confirm != nil {
		t.Error("the dialog stays open after enter")
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("the landed worktree is still there")
	}
	if !strings.Contains(plain(m), "app: removed 1") {
		t.Errorf("no status:\n%s", plain(m))
	}

	press(t, m, "d")
	press(t, m, "esc")
	if m.confirm != nil || !strings.Contains(plain(m), "nothing removed") {
		t.Errorf("esc does not cancel:\n%s", plain(m))
	}
}

func TestCustomAction(t *testing.T) {
	w := newWorld(t)
	app := w.clone("app")
	wt := filepath.Join(w.base, "wt", "feature")
	gittest.Git(t, app, "worktree", "add", "-q", "-b", "feature", wt)
	log := filepath.Join(w.base, "action.log")
	m := w.model(func(c *config.File) {
		c.Actions = []config.Action{
			{Key: "H", Name: "where", Scope: config.ScopeWorktree, Run: []string{"sh", "-c", `echo "$PWD $1 $2" > ` + log + `; echo said hello`, "sh", "{branch}", "{name}"}},
			{Key: "X", Name: "fail", Run: []string{"sh", "-c", "echo broken >&2; exit 3"}},
		}
	})
	selectRepo(t, m, "app")
	press(t, m, "enter")
	m.sel += 2 // the feature worktree, after the main one
	press(t, m, "H")
	got, _ := os.ReadFile(log)
	if strings.TrimSpace(string(got)) != wt+" feature app" {
		t.Errorf("action ran with %q", got)
	}
	if !strings.Contains(plain(m), "where: said hello") {
		t.Errorf("no output in the status:\n%s", plain(m))
	}
	press(t, m, "X")
	if !strings.Contains(plain(m), "fail: broken") {
		t.Errorf("no failure in the status:\n%s", plain(m))
	}
}
