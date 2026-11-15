package tui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/repo"
)

// ago writes t relative to now: 5m ago, 3h ago, 4d ago, then Sep 28, then
// Sep 2024 past a year.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2006")
}

var herdrWorktree = regexp.MustCompile(`/\.herdr/worktrees/[^/]+/([^/]+)$`)

// worktreeName is a short name for a worktree: the directory name, which for
// tools that keep worktrees under one directory per repository is the name
// they gave it.
func worktreeName(w repo.Worktree) string {
	if w.Main {
		return "(main)"
	}
	return pathName(w.Path)
}

func pathName(p string) string {
	if m := herdrWorktree.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return filepath.Base(p)
}

// statusSegs is STATUS: counts (3* 1+ 12% 2$) or zsh-mini-prompt's symbols.
func (m *Model) statusSegs(r *repo.Repo) segs {
	st := m.st
	var s segs
	if m.cfg.TUI.StatusStyle == config.StatusSymbol {
		var sym segs
		if r.Unstaged+r.Conflicts > 0 {
			sym = append(sym, seg{"*", st.prompt})
		}
		if r.Staged > 0 {
			sym = append(sym, seg{"+", st.ok})
		}
		if r.Untracked > 0 {
			sym = append(sym, seg{"%", st.muted})
		}
		s = sym
		if r.Stash > 0 {
			s = s.add("$", st.stash)
		}
		return s
	}
	if n := r.Unstaged + r.Conflicts; n > 0 {
		s = s.add(fmt.Sprintf("%d*", n), st.prompt)
	}
	if r.Staged > 0 {
		s = s.add(fmt.Sprintf("%d+", r.Staged), st.ok)
	}
	if r.Untracked > 0 {
		s = s.add(fmt.Sprintf("%d%%", r.Untracked), st.muted)
	}
	if r.Stash > 0 {
		s = s.add(fmt.Sprintf("%d$", r.Stash), st.stash)
	}
	return s
}

// syncSegs is SYNC: against the upstream, with ? when GitHub has moved past
// the last fetch.
func (m *Model) syncSegs(r *repo.Repo) segs {
	st := m.st
	if !r.HasOrigin() || r.Upstream == "" || !r.HasAB {
		return nil
	}
	var s segs
	switch {
	case r.Ahead > 0 && r.Behind > 0:
		s = segs{{fmt.Sprintf("<%d>%d", r.Behind, r.Ahead), st.warn}}
	case r.Behind > 0:
		s = segs{{fmt.Sprintf("<%d", r.Behind), st.accent}}
	case r.Ahead > 0:
		s = segs{{fmt.Sprintf(">%d", r.Ahead), st.worktree}}
	default:
		s = segs{{"=", st.dim}}
	}
	if m.stale(r) {
		s = append(s, seg{"?", st.prompt})
	}
	return s
}

func (m *Model) baseSegs(r *repo.Repo) segs {
	if r.BaseBehind == 0 {
		return nil
	}
	s := segs{{fmt.Sprintf("⇣%d", r.BaseBehind), m.st.accent}}
	if m.stale(r) {
		s = append(s, seg{"?", m.st.prompt})
	}
	return s
}

func (m *Model) branchesSegs(r *repo.Repo) segs {
	var s segs
	if n := len(r.Unpushed()); n > 0 {
		s = s.add(fmt.Sprintf("↑%d", n), m.st.worktree)
	}
	if n := len(r.NoUpstream()); n > 0 {
		s = s.add(fmt.Sprintf("·%d", n), m.st.muted)
	}
	if n := len(r.GoneUpstream()); n > 0 {
		s = s.add(fmt.Sprintf("✗%d", n), m.st.dim)
	}
	return s
}

func (m *Model) repoSegs(r *repo.Repo) segs {
	st := m.st
	s := segs{{r.Name, st.name}}
	if n := len(r.Worktrees); n > 1 {
		s = s.add(fmt.Sprintf("⌥%d", n), st.worktree)
	}
	switch {
	case r.Err != nil:
		s = s.add("unreadable", st.warn)
	case !r.HasOrigin():
		s = s.add("local only", st.warn)
	case r.Slug == "":
		s = s.add("not on GitHub", st.muted)
	case !strings.EqualFold(r.Slug, m.owner+"/"+r.Name):
		other := r.Slug
		if o, n, _ := strings.Cut(r.Slug, "/"); strings.EqualFold(o, m.owner) {
			other = n
		}
		s = s.add("→ "+other, st.muted)
	}
	if g, ok := m.remoteFor(r); ok && g.Archived {
		s = s.add("archived", st.dim)
	}
	return s
}

func (m *Model) branchSegs(r *repo.Repo) segs {
	switch {
	case r.Head == "" && r.Oid == "":
		return segs{{"(no commits)", m.st.dim}}
	case r.Head == "":
		return segs{{"(detached)", m.st.warn}}
	case r.Head == r.Default:
		return segs{{r.Head, m.st.dim}}
	}
	return segs{{r.Head, m.st.strong}}
}

func (m *Model) fetchedSegs(r *repo.Repo) segs {
	switch {
	case m.busy[r.Path] != "":
		return segs{{m.busy[r.Path], m.st.accent}}
	case !r.HasOrigin():
		return segs{{"-", m.st.dim}}
	case r.FetchedAt.IsZero():
		return segs{{"never", m.st.warn}}
	case m.stale(r):
		return segs{{ago(r.FetchedAt, m.now), m.st.prompt}}
	}
	return segs{{ago(r.FetchedAt, m.now), m.st.muted}}
}

func (m *Model) lastSegs(r *repo.Repo) segs {
	if r.LastCommit.IsZero() {
		return nil
	}
	return segs{{fmt.Sprintf("%-9s", ago(r.LastCommit, m.now)), m.st.muted}, {r.LastSubject, m.st.subtle}}
}

// column is one column of the list.
type column struct {
	key   string
	title string
	width int // 0 takes what is left
}

// columns fits the list to width, dropping LAST, then FETCHED, then
// BRANCHES, then narrowing REPO and BRANCH.
func (m *Model) columns(width int) []column {
	status := 13
	if m.cfg.TUI.StatusStyle == config.StatusSymbol {
		status = 6
	}
	cols := []column{
		{"repo", "REPO", 28}, {"branch", "BRANCH", 22}, {"status", "STATUS", status},
		{"sync", "SYNC", 7}, {"base", "BASE", 6}, {"branches", "BRANCHES", 11},
		{"fetched", "FETCHED", 9}, {"last", "LAST", 0},
	}
	fixed := func(cs []column) int {
		n := 2 // the selection bar
		for _, c := range cs {
			n += c.width + 2
		}
		return n
	}
	for _, drop := range []string{"last", "fetched", "branches"} {
		need := fixed(cols)
		if cols[len(cols)-1].key == "last" {
			need += 16
		}
		if need <= width {
			break
		}
		cols = cols[:len(cols)-1]
		_ = drop
	}
	if fixed(cols) > width {
		for i := range cols {
			switch cols[i].key {
			case "repo":
				cols[i].width = 20
			case "branch":
				cols[i].width = 14
			}
		}
	}
	return cols
}

func (m *Model) cell(key string, r *repo.Repo) segs {
	switch key {
	case "repo":
		return m.repoSegs(r)
	case "branch":
		return m.branchSegs(r)
	case "status":
		return m.statusSegs(r)
	case "sync":
		return m.syncSegs(r)
	case "base":
		return m.baseSegs(r)
	case "branches":
		return m.branchesSegs(r)
	case "fetched":
		return m.fetchedSegs(r)
	case "last":
		return m.lastSegs(r)
	}
	return nil
}
