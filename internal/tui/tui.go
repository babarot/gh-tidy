// Package tui is gh-tidy's screen: one owner's clones and repositories in
// one list, the preview of the row selected below it, and the popups. It
// holds what is on the screen only (the owner and the set shown, the
// cursor, what is folded, the filter); what is known of owners and clones
// is the engine's, which hands out copies and says when they change.
package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
	"github.com/babarot/gh-tidy/internal/theme"
	"github.com/babarot/gh-tidy/internal/tui/columns"
	"github.com/babarot/gh-tidy/internal/tui/keymap"
	"github.com/babarot/gh-tidy/internal/tui/statusline"
)

// Engine is what the screen needs of the engine.
type Engine interface {
	Owner() model.Owner
	Open(login string)
	SetNeeds(needs model.Need, visible []string)
	Changes() <-chan struct{}
	Detail(clone model.CloneKey, tree string) (model.Detail, bool)
	GitHubDetail(k model.RemoteKey) model.GitHubDetail
	Tick()

	Fetch(keys []model.CloneKey) (busy []model.CloneKey)
	Advance(keys []model.CloneKey) (busy []model.CloneKey)
	PlanCleanup(key model.CloneKey, opts policy.Cleanup, cwd string) (policy.Plan, error)
	Cleanup(plans map[model.CloneKey]policy.Plan, opts policy.Cleanup, cwd string) (busy []model.CloneKey)
	ClearResults()
	Clone(key model.RemoteKey) (string, error)
	ReadList()
	Refresh()
	Hold(path, doing string) bool
	Release(path string)
	Writing() int
	Activity() model.Activity
}

// All is the owner that stands for every owner, first among the owners.
const All = "all"

// Options build the screen.
type Options struct {
	Engine   Engine
	Commands Commands
	Config   config.File
	// Cwd is where gh tidy runs: d keeps the worktree holding it.
	Cwd string
	// Owners are the owners offered, besides all, in the order shown.
	Owners []string
	// Owner is the owner opened first, and Select the row the cursor
	// starts on (the clone gh tidy started in); empty for the first row.
	Owner  string
	Select string
	// Lens and PreviewHeight are what the user chose last time; zero
	// values take the defaults.
	Lens          string
	PreviewHeight int
	// Save keeps the owner, the set and the pane's height for the next
	// start; nil keeps nothing.
	Save func(owner, lens string, previewHeight int)
	Home string // written as ~ in paths
	// Roots are where clones live: a row whose name another row shares
	// says which root it is in.
	Roots []string
	Now   func() time.Time
	// NoTimers leaves out the timers, so that every command returns; for
	// tests.
	NoTimers bool
}

// The pane's height: how many lines it shows, between these.
const (
	previewDefault = 8
	previewMin     = 3
	previewMax     = 30
)

// tickEvery is how often the engine is told to read what went stale.
const tickEvery = 30 * time.Second

// Model is the screen.
type Model struct {
	eng      Engine
	cmds     Commands
	cwd      string
	actions  []config.Action
	cleanup  config.Cleanup
	keys     keymap.Map
	sym      config.Symbols
	cols     map[columns.Lens][]columns.Column
	sections []columns.Section
	themeArg string
	st       styles
	save     func(string, string, int)
	home     string
	now      func() time.Time
	noTimers bool

	owners []string // all first
	owner  string
	lens   columns.Lens

	snap   model.Owner
	lines  []line
	order  map[string]int
	cursor string
	moved  bool // the user moved the cursor since the owner was opened
	sel    int
	offset int

	expanded map[string]bool
	folds    map[string]bool

	filter    string
	filtering bool

	ph      int
	pscroll int
	pkey    string
	pfold   map[string]bool
	detail  *model.Detail
	// ghd is the GitHub detail of ghdOf, the repository selected.
	ghd   model.GitHubDetail
	ghdOf model.RemoteKey

	popup *popup

	// planSeq numbers the reads of what d and D would remove: only the
	// last one asked for is shown.
	planSeq int
	// quitting waits for the changes in flight before quitting.
	quitting bool

	line statusline.Line
	// spinning says a frame of the spinner is due: it turns while work
	// goes on.
	spinning bool
	// boxRows is how many lines the activity box took when last drawn;
	// workers says w asked for the workers' line in it.
	boxRows int
	workers bool
	// syncedBox is boxRows when the rows shown were last told to the
	// engine.
	syncedBox int

	width, height int
	dark          bool

	hits      []hit
	lastClick click
	// roots are where clones live, and twins the clones whose name
	// another clone of the list shares.
	roots []string
	twins map[string]bool
	// dragging says the pane's top line is held by the mouse.
	dragging bool
}

// New returns the screen, or what is wrong with the settings it reads.
func New(o Options) (*Model, error) {
	var errs []error
	keys, err := keymap.Apply(keymap.Default(), o.Config.Keys)
	if err != nil {
		errs = append(errs, err)
	}
	cols := map[columns.Lens][]columns.Column{}
	for lens, names := range map[columns.Lens][]string{columns.Local: o.Config.Columns.Local, columns.GitHub: o.Config.Columns.GitHub} {
		cs, bad := columns.Pick(lens, names)
		if len(bad) > 0 {
			errs = append(errs, fmt.Errorf("[columns] %s: %s not among %s", lens, strings.Join(bad, ", "), strings.Join(columns.Names(lens), ", ")))
		}
		cols[lens] = cs
	}
	secs, bad := columns.PickSections(o.Config.Preview.Sections)
	if len(bad) > 0 {
		errs = append(errs, fmt.Errorf("[preview] sections: %s not among %s", strings.Join(bad, ", "), strings.Join(columns.SectionNames(), ", ")))
	}
	for _, a := range o.Config.Actions {
		for _, s := range []keymap.Scope{keymap.ScopeGlobal, keymap.ScopeNav, keymap.ScopeList} {
			for _, act := range keys.Actions(s) {
				if keys.Matches(act, a.Key) {
					errs = append(errs, fmt.Errorf("[[actions]] %q: %q is also %s's key", a.Name, a.Key, act))
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	m := &Model{eng: o.Engine, cmds: o.Commands, cwd: o.Cwd, actions: o.Config.Actions, cleanup: o.Config.Cleanup, keys: keys, sym: o.Config.Symbols, cols: cols, sections: secs, themeArg: o.Config.TUI.Theme,
		save: o.Save, home: o.Home, now: now, noTimers: o.NoTimers, roots: o.Roots,
		owners: append([]string{All}, o.Owners...), owner: o.Owner, lens: columns.Local, cursor: o.Select,
		order: map[string]int{}, expanded: map[string]bool{}, folds: map[string]bool{}, pfold: map[string]bool{},
		ph: previewDefault, dark: true}
	if slices.Contains(columns.Lenses, columns.Lens(o.Lens)) {
		m.lens = columns.Lens(o.Lens)
	}
	if o.PreviewHeight >= previewMin && o.PreviewHeight <= previewMax {
		m.ph = o.PreviewHeight
	}
	if m.owner == "" {
		m.owner = All
	}
	if !slices.ContainsFunc(m.owners, func(s string) bool { return strings.EqualFold(s, m.owner) }) {
		m.owners = append(m.owners, m.owner)
	}
	m.st = newStyles(theme.Get(m.themeArg, m.dark))
	return m, nil
}

// changedMsg says the engine has something new.
type changedMsg struct{}

// tickMsg asks the engine to read what went stale.
type tickMsg struct{}

// statusDoneMsg takes a message off the status line.
type statusDoneMsg struct{ id int }

// frameMsg turns the spinner.
type frameMsg struct{}

// spin asks for the spinner's next frame while work goes on, and while
// the activity box or the rate limit waits to fold.
func (m *Model) spin() tea.Cmd {
	if a := m.eng.Activity(); m.noTimers || m.spinning || (!a.Busy() && !m.boxShown(a) && !m.rateShown(a)) {
		return nil
	}
	m.spinning = true
	return tea.Tick(statusline.FrameEvery, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m *Model) waitChange() tea.Cmd {
	ch := m.eng.Changes()
	return func() tea.Msg {
		<-ch
		return changedMsg{}
	}
}

func (m *Model) tick() tea.Cmd {
	if m.noTimers {
		return nil
	}
	return tea.Tick(tickEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init opens the first owner.
func (m *Model) Init() tea.Cmd {
	m.eng.Open(m.owner)
	m.refresh()
	return tea.Batch(m.waitChange(), m.tick(), tea.RequestBackgroundColor)
}

// refresh takes a new copy of the owner from the engine and lays it out.
func (m *Model) refresh() {
	m.snap = m.eng.Owner()
	m.twins = columns.Twins(m.snap.Rows)
	m.relayout()
	m.sync()
}

// sync tells the engine what the screen shows, and takes the detail of
// the row selected.
func (m *Model) sync() {
	m.scroll()
	m.syncedBox = m.boxRows
	var visible []string
	for _, l := range m.lines[m.offset:min(len(m.lines), m.offset+m.listHeight())] {
		if l.row != nil {
			visible = append(visible, l.row.Key())
		}
	}
	needs := columns.Needs(m.lens, m.cols[m.lens])
	m.eng.SetNeeds(needs, visible)
	m.detail = nil
	if l, ok := m.selected(); ok && l.row != nil && l.row.Clone != "" && l.kind != columns.KindHead {
		tree := ""
		if l.kind == columns.KindWorktree {
			tree = l.tree.Path
		}
		if d, ok := m.eng.Detail(l.row.Clone, tree); ok {
			m.detail = &d
		}
	}
	// The github pane's alerts and checks, of the repository selected.
	m.ghd, m.ghdOf = model.GitHubDetail{}, ""
	if l, ok := m.selected(); ok && l.row != nil && l.row.Remote != "" && l.kind != columns.KindHead && l.kind != columns.KindWorktree &&
		(m.lens == columns.GitHub || l.kind == columns.KindRemote) {
		m.ghd, m.ghdOf = m.eng.GitHubDetail(l.row.Remote), l.row.Remote
	}
}

func (m *Model) selected() (line, bool) {
	if m.sel < 0 || m.sel >= len(m.lines) {
		return line{}, false
	}
	return m.lines[m.sel], true
}

// Update takes a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.st = newStyles(theme.Get(m.themeArg, m.dark))
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.place()
		m.sync()
	case changedMsg:
		m.refresh()
		if m.quitting && m.eng.Writing() == 0 {
			return m, tea.Quit
		}
		return m, tea.Batch(m.waitChange(), m.spin())
	case tickMsg:
		m.eng.Tick()
		return m, tea.Batch(m.tick(), m.spin())
	case statusDoneMsg:
		m.line.Expire(msg.id)
	case frameMsg:
		m.spinning = false
		// The box folding or showing changed the list's height: the rows
		// shown are told to the engine again.
		if m.boxRows != m.syncedBox {
			m.sync()
		}
		return m, m.spin()
	case sayMsg:
		return m, m.say(msg.kind, msg.text)
	case planMsg:
		cmd := m.planned(msg)
		return m, cmd
	case actionDoneMsg:
		return m, m.actionDone(msg)
	case tea.KeyPressMsg:
		cmd := m.key(msg.String())
		m.sync()
		return m, tea.Batch(cmd, m.spin())
	case tea.MouseMsg:
		cmd := m.mouse(msg)
		m.sync()
		return m, tea.Batch(cmd, m.spin())
	}
	return m, nil
}

// say puts a message on the status line for a while.
func (m *Model) say(k statusline.Kind, text string) tea.Cmd {
	id := m.line.Say(k, text, m.now())
	if m.noTimers {
		return nil
	}
	return tea.Tick(statusline.For, func(time.Time) tea.Msg { return statusDoneMsg{id} })
}

// key acts on a key press: the fixed keys, then the popup, the filter
// being typed, and the list.
func (m *Model) key(k string) tea.Cmd {
	if k == keymap.Interrupt {
		return tea.Quit
	}
	if m.popup != nil {
		return m.popupKey(k)
	}
	if m.filtering {
		return m.filterKey(k)
	}
	if k == keymap.Back {
		switch {
		case m.filter != "":
			m.filter = ""
			m.relayout()
		case m.hasResults():
			m.eng.ClearResults()
		default:
			return m.say(statusline.Info, "press "+m.keys.First("quit")+" to quit")
		}
		return nil
	}
	on := func(a keymap.Action) bool { return m.keys.Matches(a, k) }
	switch {
	case on("quit"):
		return m.quit()
	case on("help"):
		m.open(m.helpPopup())
	case on("up"):
		m.move(-1)
	case on("down"):
		m.move(1)
	case on("top"):
		m.moveTo(0)
	case on("bottom"):
		m.moveTo(len(m.lines) - 1)
	case on("page_up"):
		m.move(-m.listHeight())
	case on("page_down"):
		m.move(m.listHeight())
	case on("expand"):
		m.toggle()
	case on("lens"):
		m.setLens(columns.Lenses[(slices.Index(columns.Lenses, m.lens)+1)%len(columns.Lenses)])
	case on("prev_owner"):
		m.stepOwner(-1)
	case on("next_owner"):
		m.stepOwner(1)
	case on("choose_owner"):
		m.open(m.ownerPopup())
	case on("search"):
		m.filtering = true
	case on("workers"):
		m.workers = !m.workers
		if m.workers {
			return m.say(statusline.Info, "workers shown while gh tidy works")
		}
		return m.say(statusline.Info, "workers hidden")
	case on("preview_grow"):
		m.setPreview(m.ph + 1)
	case on("preview_shrink"):
		m.setPreview(m.ph - 1)
	case on("preview_down"):
		m.pscroll++
	case on("preview_up"):
		m.pscroll = max(0, m.pscroll-1)
	default:
		cmd, _ := m.operate(k)
		return cmd
	}
	return nil
}

// quit quits once the changes in flight end: cut off, a merge or a
// worktree removal would leave its clone half done. ctrl+c quits at once.
func (m *Model) quit() tea.Cmd {
	n := m.eng.Writing()
	if n == 0 {
		return tea.Quit
	}
	m.quitting = true
	m.line.Hold(statusline.Working, fmt.Sprintf("quitting once %d change%s end%s · %s quits now", n, plural(n), map[bool]string{true: "s", false: ""}[n == 1], keymap.Interrupt))
	return nil
}

// hasResults reports whether rows show what was done to them.
func (m *Model) hasResults() bool {
	for _, r := range m.snap.Rows {
		if r.Op.Doing == "" && r.Op.Result != "" {
			return true
		}
	}
	return false
}

func keymapAction(a string) keymap.Action { return keymap.Action(a) }

// filterKey types into the filter: enter keeps it, esc clears it.
func (m *Model) filterKey(k string) tea.Cmd {
	switch {
	case k == keymap.Back:
		m.filter, m.filtering = "", false
	case m.keys.Matches("accept", k):
		m.filtering = false
	case m.keys.Matches("erase", k):
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case k == "space":
		m.filter += " "
	case len([]rune(k)) == 1:
		m.filter += k
	default:
		return nil
	}
	m.relayout()
	return nil
}

func (m *Model) move(d int) { m.moveTo(m.sel + d) }

func (m *Model) moveTo(i int) {
	if len(m.lines) == 0 {
		return
	}
	m.sel = max(0, min(i, len(m.lines)-1))
	m.cursor, m.moved = m.lines[m.sel].key(), true
}

// toggle opens or closes the worktrees of the clone selected, or the
// section whose head is selected. On a worktree it closes its clone's.
func (m *Model) toggle() {
	l, ok := m.selected()
	if !ok {
		return
	}
	switch l.kind {
	case columns.KindHead:
		if l.head == headUnlisted {
			return
		}
		m.folds[l.head] = !m.folded(l.head)
	case columns.KindWorktree:
		m.expanded[string(l.row.Clone)] = false
		m.cursor = l.row.Key()
	case columns.KindClone:
		if m.lens != columns.Local || !l.row.Local.Has || len(l.row.Local.Value.Linked()) == 0 {
			return
		}
		m.expanded[string(l.row.Clone)] = !m.expanded[string(l.row.Clone)]
	default:
		return
	}
	m.relayout()
}

func (m *Model) setLens(l columns.Lens) {
	if l == m.lens {
		return
	}
	// The cursor stays on the repository it is on.
	if sel, ok := m.selected(); ok {
		m.cursor, m.moved = sel.key(), true
	}
	m.lens = l
	m.order = map[string]int{}
	m.relayout()
	m.keep()
}

// stepOwner opens the owner before or after the one shown.
func (m *Model) stepOwner(d int) {
	i := slices.IndexFunc(m.owners, func(s string) bool { return strings.EqualFold(s, m.owner) })
	m.openOwner(m.owners[(i+d+len(m.owners))%len(m.owners)])
}

func (m *Model) openOwner(o string) {
	if strings.EqualFold(o, m.owner) {
		return
	}
	m.owner = o
	m.order = map[string]int{}
	m.filter, m.filtering = "", false
	m.sel, m.offset, m.cursor, m.moved = 0, 0, "", false
	m.eng.Open(o)
	m.refresh()
	m.keep()
}

func (m *Model) setPreview(h int) {
	h = max(previewMin, min(previewMax, h))
	if h == m.ph {
		return
	}
	m.ph = h
	m.keep()
}

// keep saves what the next start opens with.
func (m *Model) keep() {
	if m.save != nil {
		m.save(m.owner, string(m.lens), m.ph)
	}
}
