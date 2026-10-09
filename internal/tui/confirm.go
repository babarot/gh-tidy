package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-fleet/internal/cleanup"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
)

// confirm is the cleanup dialog: what d would remove from one clone, and
// what it keeps and why. Only enter runs it and esc or q closes it; every
// other key does nothing while it is open.
type confirm struct {
	r       *repo.Repo
	plan    *cleanup.Plan
	running bool
}

type (
	planMsg struct {
		r    *repo.Repo
		plan *cleanup.Plan
		err  error
	}
	cleanedMsg struct {
		r   *repo.Repo
		n   int
		err error
	}
)

func (m *Model) startCleanup(r *repo.Repo) tea.Cmd {
	if m.busy[r.Path] != "" {
		return nil
	}
	ctx, cfg, cwd := m.ctx, m.cfg.Cleanup, m.cwd
	return func() tea.Msg {
		p, err := cleanup.Decide(ctx, r, cfg, cwd)
		return planMsg{r, p, err}
	}
}

func (m *Model) planned(msg planMsg) tea.Cmd {
	switch {
	case msg.err != nil:
		return m.setStatus(segs{{msg.r.Name + ": " + msg.err.Error(), m.st.warn}})
	case len(msg.plan.Remove) == 0 && len(msg.plan.Keep) == 0:
		return m.setStatus(segs{{msg.r.Name + ": nothing merged into " + msg.plan.Base + " to remove", m.st.subtle}})
	}
	m.confirm = &confirm{r: msg.r, plan: msg.plan}
	return nil
}

func (m *Model) confirmKey(k string) tea.Cmd {
	c := m.confirm
	if c.running {
		return nil
	}
	switch k {
	case "esc", "q":
		m.confirm = nil
		return m.setStatus(segs{{"nothing removed", m.st.muted}})
	case "enter":
		if len(c.plan.Remove) == 0 {
			m.confirm = nil
			return nil
		}
		c.running = true
		m.busy[c.r.Path] = "cleaning"
		ctx, cfg, r, p := m.ctx, m.cfg.Cleanup, c.r, c.plan
		vars := m.vars(r, nil)
		return func() tea.Msg {
			n, err := cleanup.Run(ctx, r, p, cfg, vars)
			return cleanedMsg{repo.Scan(ctx, r.Path), n, err}
		}
	}
	return nil
}

func (m *Model) cleaned(msg cleanedMsg) tea.Cmd {
	m.confirm = nil
	delete(m.busy, msg.r.Path)
	m.replace(msg.r)
	m.clamp()
	s := segs{{fmt.Sprintf("%s: removed %d", msg.r.Name, msg.n), m.st.ok}}
	if msg.err != nil {
		s = s.add("· stopped: "+oneLine(msg.err), m.st.warn)
	}
	return tea.Batch(m.adjust(msg.r), m.setStatus(s))
}

// vars are the placeholders of custom actions and worktree_remove for r,
// and w when a worktree is selected.
func (m *Model) vars(r *repo.Repo, w *repo.Worktree) map[string]string {
	v := map[string]string{"path": r.Path, "name": r.Name, "branch": r.Head, "default": r.Default, "owner": m.owner, "repo": r.Slug}
	if r.Slug != "" {
		v["url"] = "https://" + r.Host + "/" + r.Slug
		v["owner"], _, _ = strings.Cut(r.Slug, "/")
	}
	if w != nil {
		v["path"], v["branch"] = w.Path, w.Branch
	}
	return v
}

// confirmLines draws the dialog, width wide.
func (m *Model) confirmLines(width int) []string {
	c := m.confirm
	st := m.st
	inner := width - 4
	var body []string
	line := func(s segs) { body = append(body, s.render(inner, nil)) }
	item := func(it cleanup.Item) segs {
		switch it.Kind {
		case cleanup.Branch:
			return segs{{it.Branch, st.strong}}
		case cleanup.Prunable:
			return segs{{"⌥ " + pathName(it.Path), st.worktree}, {" " + locate.Abbrev(filepath.Dir(it.Path)), st.dim}}
		}
		return segs{{"⌥ " + pathName(it.Path), st.worktree}, {" " + it.Branch, st.strong}}
	}
	line(segs{{"REMOVE", st.subtle.Bold(true)}, {fmt.Sprintf(" %d", len(c.plan.Remove)), st.dim}, {"   measured against " + c.plan.Base, st.dim}})
	maxRemove := max(3, m.height-16-min(6, len(c.plan.Keep)))
	for i, it := range c.plan.Remove {
		if i == maxRemove {
			line(segs{{fmt.Sprintf("  … %d more", len(c.plan.Remove)-i), st.dim}})
			break
		}
		line(append(append(segs{{"− ", st.warn}}, item(it)...), seg{"  " + it.Why, st.dim}))
	}
	if len(c.plan.Remove) == 0 {
		line(segs{{"  nothing", st.dim}})
	}
	if len(c.plan.Keep) > 0 {
		line(nil)
		line(segs{{"KEEP", st.subtle.Bold(true)}, {fmt.Sprintf(" %d", len(c.plan.Keep)), st.dim}})
		for i, it := range c.plan.Keep {
			if i == 6 {
				line(segs{{fmt.Sprintf("  … %d more", len(c.plan.Keep)-i), st.dim}})
				break
			}
			line(append(append(segs{{"✓ ", st.ok}}, item(it)...), seg{"  " + it.Why, st.prompt}))
		}
	}
	line(nil)
	switch {
	case c.running:
		line(segs{{"removing…", st.accent}})
	case len(c.plan.Remove) == 0:
		line(segs{{"enter", st.key}, {" close", st.muted}})
	default:
		line(segs{{fmt.Sprintf("remove %d? ", len(c.plan.Remove)), st.subtle}, {"enter", st.key}, {" yes", st.muted}, {" · ", st.rule}, {"esc", st.key}, {" no", st.muted}})
	}
	return frameColored(m.st, "clean up "+c.r.Name, body, width, st.warn)
}
