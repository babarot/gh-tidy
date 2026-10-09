package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/gitx"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
)

// doneMsg is a repository read again after something ran on it.
type doneMsg struct {
	r     *repo.Repo
	what  string // fetched, pulled, ...
	err   error
	batch bool // part of F or P, summarized when all are done
	moved bool // pull moved the branch
}

// clonedMsg is a repository cloned from the not cloned tab.
type clonedMsg struct {
	name string
	r    *repo.Repo
	err  error
}

// selectedRepo is the repository of the selected row.
func (m *Model) selectedRepo() *repo.Repo {
	if m.screen != screenList || m.tab != tabCloned {
		return nil
	}
	rows := m.rows()
	if m.sel >= len(rows) {
		return nil
	}
	return rows[m.sel].r
}

func fetch(ctx context.Context, r *repo.Repo) error {
	_, err := gitx.Run(ctx, r.Path, "fetch", "--prune", "--quiet", "origin")
	return err
}

func pull(ctx context.Context, r *repo.Repo) error {
	_, err := gitx.Run(ctx, r.Path, "pull", "--ff-only", "--quiet")
	return err
}

func (m *Model) startFetch(r *repo.Repo, batch bool, sem chan struct{}) tea.Cmd {
	if !r.HasOrigin() || m.busy[r.Path] != "" {
		return nil
	}
	m.busy[r.Path] = "fetching"
	ctx := m.ctx
	return func() tea.Msg {
		if sem != nil {
			sem <- struct{}{}
			defer func() { <-sem }()
		}
		err := fetch(ctx, r)
		return doneMsg{r: repo.Scan(ctx, r.Path), what: "fetched", err: err, batch: batch}
	}
}

func (m *Model) startPull(r *repo.Repo) tea.Cmd {
	if why := r.PullBlocker(); why != "" {
		return m.setStatus(segs{{r.Name + ": not pulled, " + why, m.st.prompt}})
	}
	if m.busy[r.Path] != "" {
		return nil
	}
	m.busy[r.Path] = "pulling"
	ctx := m.ctx
	return func() tea.Msg {
		before := r.Oid
		err := pull(ctx, r)
		after := repo.Scan(ctx, r.Path)
		return doneMsg{r: after, what: "pulled", err: err, moved: after.Oid != before}
	}
}

// startPullAll fetches every repository, then pulls the ones that can move
// forward with nothing in the way; the others are left as they are.
func (m *Model) startPullAll() tea.Cmd {
	sem := make(chan struct{}, m.cfg.Core.FetchConcurrency)
	var cmds []tea.Cmd
	ctx := m.ctx
	for _, r := range m.repos {
		if !r.HasOrigin() || m.busy[r.Path] != "" {
			continue
		}
		m.busy[r.Path] = "fetching"
		m.pending++
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := fetch(ctx, r); err != nil {
				return doneMsg{r: repo.Scan(ctx, r.Path), what: "fetched", err: err, batch: true}
			}
			fresh := repo.Scan(ctx, r.Path)
			if !fresh.CanFastForward() {
				return doneMsg{r: fresh, what: "fetched", batch: true}
			}
			err := pull(ctx, fresh)
			after := repo.Scan(ctx, r.Path)
			return doneMsg{r: after, what: "pulled", err: err, batch: true, moved: after.Oid != fresh.Oid}
		})
	}
	m.batchWhat, m.batchErrs, m.batchMoved = "pull", nil, 0
	return tea.Batch(cmds...)
}

func (m *Model) startFetchAll() tea.Cmd {
	sem := make(chan struct{}, m.cfg.Core.FetchConcurrency)
	var cmds []tea.Cmd
	for _, r := range m.repos {
		if c := m.startFetch(r, true, sem); c != nil {
			m.pending++
			cmds = append(cmds, c)
		}
	}
	m.batchWhat, m.batchErrs, m.batchMoved = "fetch", nil, 0
	return tea.Batch(cmds...)
}

// done takes a repository read again after an action.
func (m *Model) done(msg doneMsg) tea.Cmd {
	delete(m.busy, msg.r.Path)
	m.replace(msg.r)
	cmds := []tea.Cmd{m.adjust(msg.r)}
	if msg.batch {
		m.pending--
		if msg.err != nil {
			m.batchErrs = append(m.batchErrs, msg.r.Name)
		}
		if msg.moved {
			m.batchMoved++
		}
		if m.pending == 0 {
			cmds = append(cmds, m.batchStatus())
		}
		return tea.Batch(cmds...)
	}
	switch {
	case msg.what == "quiet":
	case msg.err != nil:
		cmds = append(cmds, m.setStatus(segs{{msg.r.Name + ": " + oneLine(msg.err), m.st.warn}}))
	case msg.what == "pulled" && !msg.moved:
		cmds = append(cmds, m.setStatus(segs{{msg.r.Name + ": already up to date", m.st.subtle}}))
	default:
		cmds = append(cmds, m.setStatus(segs{{msg.what + " " + msg.r.Name, m.st.ok}}))
	}
	return tea.Batch(cmds...)
}

func (m *Model) batchStatus() tea.Cmd {
	var s segs
	switch m.batchWhat {
	case "pull":
		s = segs{{fmt.Sprintf("fetched all; pulled %d", m.batchMoved), m.st.ok}}
	default:
		s = segs{{"fetched all", m.st.ok}}
	}
	if n := len(m.batchErrs); n > 0 {
		s = s.add(fmt.Sprintf("· %d failed: %s", n, strings.Join(m.batchErrs, ", ")), m.st.warn)
	}
	return m.setStatus(s)
}

// replace swaps in r read again, keeping its place.
func (m *Model) replace(r *repo.Repo) {
	for i, x := range m.repos {
		if x.Path == r.Path {
			m.repos[i] = r
			return
		}
	}
	m.repos = append(m.repos, r)
}

func (m *Model) startClone() tea.Cmd {
	nc := m.notCloned()
	if m.sel >= len(nc) {
		return nil
	}
	g := nc[m.sel]
	dir := filepath.Join(m.root, m.host, m.owner, g.Name)
	if m.busy[dir] != "" {
		return nil
	}
	m.busy[dir] = "cloning"
	ctx := m.ctx
	status := m.setStatus(segs{{"cloning " + g.NameWithOwner + " into " + locate.Abbrev(dir) + "…", m.st.accent}})
	return tea.Batch(status, func() tea.Msg {
		out, err := exec.CommandContext(ctx, "gh", "repo", "clone", g.NameWithOwner, dir, "--", "--quiet").CombinedOutput()
		if err != nil {
			return clonedMsg{name: g.NameWithOwner, err: fmt.Errorf("%s", strings.TrimSpace(string(out)))}
		}
		return clonedMsg{name: g.NameWithOwner, r: repo.Scan(ctx, dir)}
	})
}

func (m *Model) cloned(msg clonedMsg) tea.Cmd {
	if msg.r != nil {
		delete(m.busy, msg.r.Path)
		m.replace(msg.r)
		m.clamp()
		return tea.Batch(m.adjust(msg.r), m.setStatus(segs{{"cloned " + msg.name + " into " + locate.Abbrev(msg.r.Path), m.st.ok}}))
	}
	for k, v := range m.busy {
		if v == "cloning" && strings.HasSuffix(k, "/"+strings.SplitN(msg.name, "/", 2)[1]) {
			delete(m.busy, k)
		}
	}
	return m.setStatus(segs{{msg.name + ": " + oneLine(msg.err), m.st.warn}})
}

// oneLine is the last line of err, where git puts what went wrong.
func oneLine(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// actionMsg is a custom action that finished.
type actionMsg struct {
	name string
	path string // the clone, read again
	out  string
	err  error
}

// startAction runs the i-th [[actions]] on the selected repository or
// worktree.
func (m *Model) startAction(i int) tea.Cmd {
	if m.screen != screenList || m.tab != tabCloned {
		return nil
	}
	rows := m.rows()
	if m.sel >= len(rows) {
		return nil
	}
	a := m.cfg.Actions[i]
	r, w := rows[m.sel].r, rows[m.sel].wt
	if a.Scope != config.ScopeWorktree {
		w = nil
	}
	vars := m.vars(r, w)
	argv := config.Expand(a.Run, vars)
	dir := vars["path"]
	name := a.Name
	if name == "" {
		name = argv[0]
	}
	cmd := exec.CommandContext(m.ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if a.Interactive {
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return actionMsg{name: name, path: r.Path, err: err} })
	}
	return tea.Batch(m.setStatus(segs{{name + "…", m.st.accent}}), func() tea.Msg {
		out, err := cmd.CombinedOutput()
		return actionMsg{name: name, path: r.Path, out: strings.TrimSpace(string(out)), err: err}
	})
}

func (m *Model) actionDone(msg actionMsg) tea.Cmd {
	var s segs
	switch {
	case msg.err != nil && msg.out != "":
		s = segs{{msg.name + ": " + oneLine(fmt.Errorf("%s", msg.out)), m.st.warn}}
	case msg.err != nil:
		s = segs{{msg.name + ": " + msg.err.Error(), m.st.warn}}
	case msg.out != "":
		s = segs{{msg.name + ": " + oneLine(fmt.Errorf("%s", msg.out)), m.st.ok}}
	default:
		s = segs{{msg.name + ": done", m.st.ok}}
	}
	// The command may have changed the clone.
	ctx, path := m.ctx, msg.path
	return tea.Batch(m.setStatus(s), func() tea.Msg { return doneMsg{r: repo.Scan(ctx, path), what: "quiet"} })
}
