// Package tui is the interactive list of one owner's clones.
package tui

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/github"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
	"github.com/babarot/gh-fleet/internal/theme"
)

type screen int

const (
	screenList   screen = iota // one owner's clones
	screenPicker               // the owners under root
)

type tab int

const (
	tabCloned tab = iota
	tabNotCloned
)

// Model is the TUI.
type Model struct {
	ctx  context.Context
	cfg  config.File
	keys keymap
	st   styles
	now  time.Time

	root, host, owner string
	screen            screen
	tab               tab
	selectPath        string // the clone started in, selected once it arrives

	repos      []*repo.Repo
	scanning   bool
	scanTotal  int
	scanCh     chan *repo.Repo
	remote     map[string]github.Repo // the clones' repositories, by lower-case owner/name
	remoteErr  error
	remoteDone bool
	list       []github.Repo // every repository of the owner, for not cloned
	listErr    error
	listDone   bool

	owners []locate.OwnerDir

	sel, offset int
	expanded    map[string]bool
	filter      string
	filtering   bool
	showAll     bool
	sortIdx     int
	help        bool
	busy        map[string]string // path -> what is running on it

	// F and P: how many are still running, and what to say when none are.
	pending    int
	batchWhat  string
	batchErrs  []string
	batchMoved int

	status   segs
	statusID int

	width, height int

	cwd     string // where gh fleet runs; d keeps the worktree holding it
	confirm *confirm

	noTimers bool // tests: no ticks, statuses stay
}

// New returns the TUI for target.
func New(ctx context.Context, cfg config.File, root string, target locate.Target) (*Model, error) {
	km, err := newKeymap(cfg)
	if err != nil {
		return nil, err
	}
	m := &Model{ctx: ctx, cfg: cfg, keys: km, st: newStyles(theme.Get(cfg.TUI.Theme, true)), now: time.Now(),
		root: root, host: target.Host, expanded: map[string]bool{}, busy: map[string]string{},
		sortIdx: max(0, slices.Index(config.Sorts, cfg.TUI.Sort))}
	if wd, err := os.Getwd(); err == nil {
		m.cwd, _ = filepath.EvalSymlinks(wd)
	}
	if target.Kind == locate.Picker {
		m.screen = screenPicker
		m.owners, _ = locate.Owners(root, target.Host)
	} else {
		m.owner, m.selectPath = target.Owner, target.Select
	}
	return m, nil
}

// Init starts reading the owner's clones and GitHub.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor, m.tick()}
	if m.screen == screenList {
		cmds = append(cmds, m.open(m.owner))
	}
	return tea.Batch(cmds...)
}

type (
	repoMsg      struct{ r *repo.Repo }
	scanDoneMsg  struct{}
	scanStartMsg struct {
		paths []string
		err   error
	}
	remoteMsg struct {
		owner string
		repos map[string]github.Repo
		err   error
	}
	listMsg struct {
		owner string
		repos []github.Repo
		err   error
	}
	adjustedMsg  struct{ r *repo.Repo }
	tickMsg      time.Time
	statusExpMsg struct{ id int }
)

func (m *Model) tick() tea.Cmd {
	if m.noTimers {
		return nil
	}
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// open switches to owner and starts reading its clones and, unless
// stale_check is off, its repositories on GitHub.
func (m *Model) open(owner string) tea.Cmd {
	m.owner, m.screen, m.tab = owner, screenList, tabCloned
	m.repos, m.remote, m.remoteErr, m.remoteDone = nil, nil, nil, false
	m.list, m.listErr, m.listDone = nil, nil, false
	m.sel, m.offset, m.filter = 0, 0, ""
	m.scanning = true
	dir := filepath.Join(m.root, m.host, owner)
	cmds := []tea.Cmd{func() tea.Msg {
		paths, err := repo.Find(dir)
		return scanStartMsg{paths, err}
	}}
	if m.cfg.Core.StaleCheck {
		host, ctx := m.host, m.ctx
		cmds = append(cmds, func() tea.Msg {
			rs, err := github.List(ctx, host, owner)
			return listMsg{owner, rs, err}
		})
	} else {
		m.remoteDone, m.listDone = true, true
	}
	return tea.Batch(cmds...)
}

func (m *Model) waitScan() tea.Cmd {
	ch := m.scanCh
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return scanDoneMsg{}
		}
		return repoMsg{r}
	}
}

// Update handles a message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.st = newStyles(theme.Get(m.cfg.TUI.Theme, msg.IsDark()))
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
	case tickMsg:
		m.now = time.Time(msg)
		return m, m.tick()
	case scanStartMsg:
		if msg.err != nil && !errors.Is(msg.err, fs.ErrNotExist) {
			m.setStatus(segs{{msg.err.Error(), m.st.warn}})
		}
		m.scanTotal = len(msg.paths)
		if len(msg.paths) == 0 {
			m.scanning = false
			return m, m.afterLoad()
		}
		m.scanCh = make(chan *repo.Repo)
		ch, ctx, paths := m.scanCh, m.ctx, msg.paths
		go func() {
			repo.ScanAll(ctx, paths, runtime.GOMAXPROCS(0)*2, func(r *repo.Repo) { ch <- r })
			close(ch)
		}()
		return m, m.waitScan()
	case repoMsg:
		m.repos = append(m.repos, msg.r)
		return m, tea.Batch(m.waitScan(), m.adjust(msg.r))
	case scanDoneMsg:
		m.scanning = false
		return m, tea.Batch(m.lookup(), m.afterLoad())
	case remoteMsg:
		if msg.owner != m.owner {
			return m, nil
		}
		m.remoteDone, m.remoteErr, m.remote = true, msg.err, msg.repos
		var cmds []tea.Cmd
		for _, r := range m.repos {
			cmds = append(cmds, m.adjust(r))
		}
		if msg.err != nil {
			cmds = append(cmds, m.setStatus(segs{{"GitHub: " + msg.err.Error(), m.st.warn}}))
		}
		return m, tea.Batch(cmds...)
	case listMsg:
		if msg.owner != m.owner {
			return m, nil
		}
		m.listDone, m.listErr, m.list = true, msg.err, msg.repos
		slices.SortFunc(m.list, func(a, b github.Repo) int { return b.PushedAt.Compare(a.PushedAt) })
		cmds := []tea.Cmd{m.afterLoad()}
		if msg.err != nil {
			text := "GitHub: " + msg.err.Error()
			if errors.Is(msg.err, github.ErrNoOwner) {
				text = m.owner + ": " + msg.err.Error()
			}
			cmds = append(cmds, m.setStatus(segs{{text, m.st.warn}}))
		}
		return m, tea.Batch(cmds...)
	case adjustedMsg:
		for i, r := range m.repos {
			if r.Path == msg.r.Path {
				m.repos[i] = msg.r
			}
		}
	case doneMsg:
		return m, m.done(msg)
	case clonedMsg:
		return m, m.cloned(msg)
	case planMsg:
		return m, m.planned(msg)
	case cleanedMsg:
		return m, m.cleaned(msg)
	case statusExpMsg:
		if msg.id == m.statusID {
			m.status = nil
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	return m, nil
}

// lookup asks GitHub for the clones' repositories, once every clone is
// read, to know their default branches and whether GitHub has moved past
// them.
func (m *Model) lookup() tea.Cmd {
	if !m.cfg.Core.StaleCheck {
		return nil
	}
	var slugs []string
	for _, r := range m.repos {
		if r.Slug != "" && r.Host == m.host {
			slugs = append(slugs, r.Slug)
		}
	}
	if len(slugs) == 0 {
		m.remoteDone = true
		return nil
	}
	ctx, host, owner := m.ctx, m.host, m.owner
	return func() tea.Msg {
		rs, err := github.Lookup(ctx, host, slugs)
		return remoteMsg{owner, rs, err}
	}
}

// afterLoad opens the not cloned tab for an owner with no clones, once both
// sides are known.
func (m *Model) afterLoad() tea.Cmd {
	if m.scanning || !m.listDone || len(m.repos) > 0 {
		return nil
	}
	if m.listErr == nil && len(m.notCloned()) > 0 {
		m.tab = tabNotCloned
		return m.setStatus(segs{{"no clones under " + locate.Abbrev(filepath.Join(m.root, m.host, m.owner)) + " yet", m.st.subtle}})
	}
	return nil
}

// adjust measures r against GitHub's default branch when it differs from
// the local one, on a copy, so the list keeps drawing the old one meanwhile.
func (m *Model) adjust(r *repo.Repo) tea.Cmd {
	g, ok := m.remoteFor(r)
	if !ok || g.Default == "" || (g.Default == r.Default && r.DefaultSource == repo.DefaultFromGitHub) {
		return nil
	}
	cp := *r
	ctx := m.ctx
	return func() tea.Msg {
		cp.SetDefault(ctx, g.Default)
		return adjustedMsg{&cp}
	}
}

func (m *Model) remoteFor(r *repo.Repo) (github.Repo, bool) {
	if m.remote == nil || r.Slug == "" {
		return github.Repo{}, false
	}
	g, ok := m.remote[strings.ToLower(r.Slug)]
	return g, ok
}

// stale reports whether GitHub's default branch has moved past r's last
// fetch, so its counts are out of date.
func (m *Model) stale(r *repo.Repo) bool {
	g, ok := m.remoteFor(r)
	return ok && g.Default == r.Default && r.IsStale(g.DefaultOid)
}

// notCloned lists the owner's repositories on GitHub, archived ones left
// out, that no clone's origin points to.
func (m *Model) notCloned() []github.Repo {
	have := map[string]bool{}
	for _, r := range m.repos {
		have[strings.ToLower(r.Slug)] = true
	}
	var out []github.Repo
	for _, g := range m.list {
		if !g.Archived && !have[strings.ToLower(g.NameWithOwner)] {
			out = append(out, g)
		}
	}
	if m.filter != "" {
		out = slices.DeleteFunc(out, func(g github.Repo) bool { return !strings.Contains(strings.ToLower(g.Name), strings.ToLower(m.filter)) })
	}
	return out
}

// hidden reports whether r is left out until show_all.
func (m *Model) hidden(r *repo.Repo) bool {
	if m.showAll || r.Path == m.selectPath {
		return false
	}
	return (r.Quiet() && m.cfg.Hides(config.HideQuiet)) || (r.UntrackedOnly() && m.cfg.Hides(config.HideUntracked))
}

// visible returns the clones to list, filtered and sorted.
func (m *Model) visible() []*repo.Repo {
	var out []*repo.Repo
	f := strings.ToLower(m.filter)
	for _, r := range m.repos {
		if m.hidden(r) {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.Head), f) {
			continue
		}
		out = append(out, r)
	}
	byName := func(a, b *repo.Repo) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }
	slices.SortStableFunc(out, func(a, b *repo.Repo) int {
		switch config.Sorts[m.sortIdx] {
		case config.SortAttention:
			if d := b.Score() - a.Score(); d != 0 {
				return d
			}
		case config.SortFetched:
			if c := a.FetchedAt.Compare(b.FetchedAt); c != 0 {
				return c
			}
		case config.SortLastCommit:
			if c := b.LastCommit.Compare(a.LastCommit); c != 0 {
				return c
			}
		}
		return byName(a, b)
	})
	return out
}

// row is a line of the list: a clone, or one of its worktrees under it.
type row struct {
	r    *repo.Repo
	wt   *repo.Worktree
	last bool // the last worktree, drawn with └
}

func (m *Model) rows() []row {
	var rows []row
	for _, r := range m.visible() {
		rows = append(rows, row{r: r})
		if m.expanded[r.Path] {
			for i := range r.Worktrees {
				rows = append(rows, row{r: r, wt: &r.Worktrees[i], last: i == len(r.Worktrees)-1})
			}
		}
	}
	return rows
}

func (m *Model) count() int {
	switch {
	case m.screen == screenPicker:
		return len(m.pickerOwners())
	case m.tab == tabNotCloned:
		return len(m.notCloned())
	}
	return len(m.rows())
}

func (m *Model) pickerOwners() []locate.OwnerDir {
	if m.filter == "" {
		return m.owners
	}
	return slices.DeleteFunc(slices.Clone(m.owners), func(o locate.OwnerDir) bool {
		return !strings.Contains(strings.ToLower(o.Name), strings.ToLower(m.filter))
	})
}

func (m *Model) clamp() {
	n := m.count()
	m.sel = max(0, min(m.sel, n-1))
	h := m.listHeight()
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if h > 0 && m.sel >= m.offset+h {
		m.offset = m.sel - h + 1
	}
	m.offset = max(0, min(m.offset, max(0, n-h)))
}

func (m *Model) setStatus(s segs) tea.Cmd {
	m.status = s
	m.statusID++
	id := m.statusID
	if m.noTimers {
		return nil
	}
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return statusExpMsg{id} })
}

func (m *Model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != nil {
		return m, m.confirmKey(k)
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if m.filtering {
		switch k {
		case "esc":
			m.filtering, m.filter = false, ""
		case "enter":
			m.filtering = false
		case "backspace":
			if r := []rune(m.filter); len(r) > 0 {
				m.filter = string(r[:len(r)-1])
			}
		default:
			if t := msg.Key().Text; t != "" {
				m.filter += t
				m.sel, m.offset = 0, 0
			}
		}
		m.clamp()
		return m, nil
	}
	if k == "esc" && m.filter != "" {
		m.filter = ""
		m.clamp()
		return m, nil
	}

	op := m.keys.op(k)
	var cmd tea.Cmd
	switch op {
	case opQuit:
		return m, tea.Quit
	case opHelp:
		m.help = true
	case opUp:
		m.sel--
	case opDown:
		m.sel++
	case opTop:
		m.sel = 0
	case opBottom:
		m.sel = m.count() - 1
	case opPageUp:
		m.sel -= max(1, m.listHeight()-1)
	case opPageDown:
		m.sel += max(1, m.listHeight()-1)
	case opSearch:
		m.filtering = true
	case opShowAll:
		if m.screen == screenList {
			m.showAll = !m.showAll
		}
	case opSort:
		if m.screen == screenList {
			m.sortIdx = (m.sortIdx + 1) % len(config.Sorts)
		}
	case opSwitchTab:
		if m.screen == screenList {
			m.tab = 1 - m.tab
			m.sel, m.offset = 0, 0
		}
	case opExpand:
		cmd = m.expand()
	case opFetch:
		if r := m.selectedRepo(); r != nil {
			if !r.HasOrigin() {
				cmd = m.setStatus(segs{{r.Name + ": no origin to fetch", m.st.prompt}})
			} else {
				cmd = m.startFetch(r, false, nil)
			}
		}
	case opFetchAll:
		if m.screen == screenList && !m.scanning {
			cmd = m.startFetchAll()
		}
	case opPull:
		if r := m.selectedRepo(); r != nil {
			cmd = m.startPull(r)
		}
	case opPullAll:
		if m.screen == screenList && !m.scanning {
			cmd = m.startPullAll()
		}
	case opCleanup:
		if r := m.selectedRepo(); r != nil {
			cmd = m.startCleanup(r)
		}
	case opClone:
		if m.screen == screenList && m.tab == tabNotCloned {
			cmd = m.startClone()
		}
	case opOpenBrowser:
		if u := m.selectedURL(); u != "" {
			cmd = runQuiet(openCommand(u))
		}
	case opReveal:
		if p := m.selectedPath(); p != "" && runtime.GOOS == "darwin" {
			cmd = runQuiet([]string{"open", "-R", p})
		}
	case opCopyPath:
		if p := m.selectedPath(); p != "" {
			cmd = tea.Batch(copyCmd(p), m.setStatus(segs{{"copied " + locate.Abbrev(p), m.st.ok}}))
		}
	case opCopyURL:
		if u := m.selectedURL(); u != "" {
			cmd = tea.Batch(copyCmd(u), m.setStatus(segs{{"copied " + u, m.st.ok}}))
		}
	}
	m.clamp()
	return m, cmd
}

func (m *Model) expand() tea.Cmd {
	if m.screen == screenPicker {
		owners := m.pickerOwners()
		if m.sel < len(owners) {
			return m.open(owners[m.sel].Name)
		}
		return nil
	}
	if m.tab != tabCloned {
		return nil
	}
	rows := m.rows()
	if m.sel >= len(rows) {
		return nil
	}
	r := rows[m.sel].r
	m.expanded[r.Path] = !m.expanded[r.Path]
	if !m.expanded[r.Path] {
		// Back to the repository's own row when its worktrees fold away.
		for i, x := range m.rows() {
			if x.r == r && x.wt == nil {
				m.sel = i
			}
		}
	}
	return nil
}

// selectedPath is the path of the selected worktree or clone.
func (m *Model) selectedPath() string {
	if m.screen != screenList || m.tab != tabCloned {
		return ""
	}
	rows := m.rows()
	if m.sel >= len(rows) {
		return ""
	}
	if w := rows[m.sel].wt; w != nil {
		return w.Path
	}
	return rows[m.sel].r.Path
}

// selectedURL is the selected repository on GitHub.
func (m *Model) selectedURL() string {
	if m.screen == screenPicker {
		owners := m.pickerOwners()
		if m.sel < len(owners) {
			return "https://" + m.host + "/" + owners[m.sel].Name
		}
		return ""
	}
	if m.tab == tabNotCloned {
		nc := m.notCloned()
		if m.sel < len(nc) {
			return "https://" + m.host + "/" + nc[m.sel].NameWithOwner
		}
		return ""
	}
	rows := m.rows()
	if m.sel >= len(rows) || rows[m.sel].r.Slug == "" {
		return ""
	}
	r := rows[m.sel].r
	return "https://" + r.Host + "/" + r.Slug
}

func openCommand(url string) []string {
	if runtime.GOOS == "darwin" {
		return []string{"open", url}
	}
	return []string{"xdg-open", url}
}

func runQuiet(argv []string) tea.Cmd {
	return func() tea.Msg {
		_ = exec.Command(argv[0], argv[1:]...).Run()
		return nil
	}
}

func copyCmd(text string) tea.Cmd {
	// OSC 52 works over SSH and in most terminals; pbcopy covers terminals
	// that ignore it on macOS.
	cmds := []tea.Cmd{tea.SetClipboard(text)}
	if runtime.GOOS == "darwin" {
		if path, err := exec.LookPath("pbcopy"); err == nil {
			cmds = append(cmds, func() tea.Msg {
				c := exec.Command(path)
				c.Stdin = strings.NewReader(text)
				_ = c.Run()
				return nil
			})
		}
	}
	return tea.Batch(cmds...)
}
