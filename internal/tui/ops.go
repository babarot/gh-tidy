package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/policy"
	"github.com/babarot/gh-tidy/internal/tui/columns"
)

// Commands are the programs other than git the screen runs: the user's
// actions, and the system's ways to open a URL, reveal a file and copy.
type Commands interface {
	Run(ctx context.Context, dir string, argv []string) (string, error)
	// Interactive is argv to be handed the terminal.
	Interactive(dir string, argv []string) tea.ExecCommand
	OpenURL(url string) error
	Reveal(path string) error
	Copy(text string) error
}

// planMsg is what d or D would remove, read for the confirm.
type planMsg struct {
	seq   int
	owner string
	plans map[model.CloneKey]policy.Plan
	names map[model.CloneKey]string
	bulk  bool
	err   error
}

// actionDoneMsg is a custom action that ended.
type actionDoneMsg struct {
	name, path, out string
	err             error
}

// target is the clone a key acts on: the clone selected, or the clone of
// the worktree selected.
func (m *Model) target() (*model.Row, bool) {
	l, ok := m.selected()
	if !ok || l.row == nil || l.row.Clone == "" {
		return nil, false
	}
	return l.row, true
}

// clones are the clones of the owner shown (every owner's, in all).
func (m *Model) clones(keep func(*model.Row) bool) []model.CloneKey {
	var ks []model.CloneKey
	for i := range m.snap.Rows {
		r := &m.snap.Rows[i]
		if r.Clone != "" && r.Local.Has && (keep == nil || keep(r)) {
			ks = append(ks, r.Clone)
		}
	}
	return ks
}

func hasOrigin(r *model.Row) bool { return r.Local.Value.HasOrigin() }

// busySaid says how many clones a change left alone because another was
// in flight in them.
func (m *Model) busySaid(what string, busy []model.CloneKey) tea.Cmd {
	if len(busy) == 0 {
		return nil
	}
	return m.say(seg{fmt.Sprintf("%s: %d busy with another change, left alone", what, len(busy)), rolePrompt})
}

func (m *Model) cleanupOpts(bulk bool) policy.Cleanup {
	return policy.Cleanup{Protect: m.cleanup.Protect, DeleteGone: m.cleanup.DeleteGone, Bulk: bulk}
}

// operate runs the action of a key that changes something or reaches
// outside; ok is false for a key that is none of them.
func (m *Model) operate(k string) (tea.Cmd, bool) {
	on := func(a string) bool { return m.keys.Matches(keymapAction(a), k) }
	switch {
	case on("fetch"):
		r, ok := m.target()
		if !ok {
			return m.say(seg{"select a clone to fetch", roleMuted}), true
		}
		return m.busySaid("fetch", m.eng.Fetch([]model.CloneKey{r.Clone})), true
	case on("fetch_all"):
		ks := m.clones(hasOrigin)
		return tea.Batch(m.say(seg{fmt.Sprintf("fetching %d clones", len(ks)), roleAccent}), m.busySaid("fetch", m.eng.Fetch(ks))), true
	case on("pull"):
		r, ok := m.target()
		if !ok {
			return m.say(seg{"select a clone to update", roleMuted}), true
		}
		return m.busySaid("update", m.eng.Advance([]model.CloneKey{r.Clone})), true
	case on("pull_all"):
		m.open(m.updateAllPopup())
		return nil, true
	case on("cleanup"):
		r, ok := m.target()
		if !ok {
			return m.say(seg{"select a clone to clean up", roleMuted}), true
		}
		return m.plan([]*model.Row{r}, false), true
	case on("cleanup_all"):
		var rs []*model.Row
		for i := range m.snap.Rows {
			r := &m.snap.Rows[i]
			if r.Clone != "" && r.Local.Has && len(r.Local.Value.Linked()) > 0 {
				rs = append(rs, r)
			}
		}
		if len(rs) == 0 {
			return m.say(seg{"no clone has worktrees", roleMuted}), true
		}
		return m.plan(rs, true), true
	case on("clone"):
		l, ok := m.selected()
		if !ok || l.kind != columns.KindRemote {
			return m.say(seg{"select a repository not cloned to clone it", roleMuted}), true
		}
		dir, err := m.eng.Clone(l.row.Remote)
		if err != nil {
			return m.say(seg{err.Error(), roleWarn}), true
		}
		return m.say(seg{"cloning into " + m.short(dir), roleAccent}), true
	case on("refresh"):
		m.eng.Refresh()
		return m.say(seg{"reading " + m.owner + " again", roleAccent}), true
	case on("list"):
		if m.snap.All {
			return m.say(seg{"choose one owner to read its list", roleMuted}), true
		}
		m.eng.ReadList()
		return m.say(seg{"reading the list of " + m.owner, roleAccent}), true
	case on("open_browser"):
		u := m.url()
		if u == "" {
			return m.say(seg{"not on GitHub", roleMuted}), true
		}
		return m.outside(func() error { return m.cmds.OpenURL(u) }), true
	case on("copy_path"):
		p := m.path()
		if p == "" {
			return nil, true
		}
		return tea.Batch(tea.SetClipboard(p), m.outside(func() error { return m.cmds.Copy(p) }), m.say(seg{"copied " + m.short(p), roleOK})), true
	case on("copy_url"):
		u := m.url()
		if u == "" {
			return m.say(seg{"not on GitHub", roleMuted}), true
		}
		return tea.Batch(tea.SetClipboard(u), m.outside(func() error { return m.cmds.Copy(u) }), m.say(seg{"copied " + u, roleOK})), true
	case on("reveal"):
		p := m.path()
		if p == "" {
			return nil, true
		}
		return m.outside(func() error { return m.cmds.Reveal(p) }), true
	}
	for i, a := range m.actions {
		if a.Key == k {
			return m.action(i), true
		}
	}
	return nil, false
}

// outside runs f off the screen's goroutine, saying only what went wrong.
func (m *Model) outside(f func() error) tea.Cmd {
	return func() tea.Msg {
		if err := f(); err != nil {
			return sayMsg{seg{err.Error(), roleWarn}}
		}
		return nil
	}
}

// sayMsg puts a message on the status line from a command.
type sayMsg struct{ s seg }

func (m *Model) short(p string) string {
	if m.home != "" && strings.HasPrefix(p, m.home+"/") {
		return "~" + p[len(m.home):]
	}
	return p
}

// path is the selected worktree's or clone's directory.
func (m *Model) path() string {
	l, ok := m.selected()
	switch {
	case !ok || l.row == nil:
		return ""
	case l.kind == columns.KindWorktree:
		return l.tree.Path
	}
	return string(l.row.Clone)
}

// url is the selected row's repository on GitHub.
func (m *Model) url() string {
	l, ok := m.selected()
	if !ok || l.row == nil || l.row.Remote == "" {
		return ""
	}
	return "https://" + string(l.row.Remote)
}

// updateAllPopup asks before P: it fetches every clone and moves the
// default branches policy allows.
func (m *Model) updateAllPopup() *popup {
	ks := m.clones(hasOrigin)
	can, blocked := 0, 0
	for i := range m.snap.Rows {
		r := &m.snap.Rows[i]
		if r.Clone == "" || !r.Local.Has {
			continue
		}
		switch s := columns.Main(r); {
		case s.CanUpdate():
			can++
		case s.Blocked != "" && (s.Kind == columns.MainBehind || s.GitHubAhead):
			blocked++
		}
	}
	scope := m.owner
	if m.snap.All {
		scope = "every owner"
	}
	body := []drawn{
		{parts: []cell{{text: fmt.Sprintf("Fetch the %d clones of %s, then fast-forward each default branch", len(ks), scope), role: roleText}}},
		{parts: []cell{{text: "where nothing is in the way. Branches being worked on are not touched.", role: roleText}}},
		{},
		{parts: []cell{{text: fmt.Sprintf("as last read: %d can update", can), role: roleAccent}, {text: fmt.Sprintf(" · %d in the way", blocked), role: rolePrompt}}},
		{parts: []cell{{text: "each is decided again after its fetch", role: roleMuted}}},
	}
	return m.confirmPopup("update all", body, func(m *Model) tea.Cmd {
		return tea.Batch(m.say(seg{fmt.Sprintf("updating %d clones", len(ks)), roleAccent}), m.busySaid("update", m.eng.Advance(ks)))
	})
}

// plan reads what d (or D, bulk) would do to rs, for the confirm.
func (m *Model) plan(rs []*model.Row, bulk bool) tea.Cmd {
	opts := m.cleanupOpts(bulk)
	keys := make([]model.CloneKey, len(rs))
	names := map[model.CloneKey]string{}
	for i, r := range rs {
		keys[i] = r.Clone
		names[r.Clone] = line{kind: columns.KindClone, row: r}.name()
	}
	eng, cwd := m.eng, m.cwd
	m.planSeq++
	seq, owner := m.planSeq, m.owner
	return tea.Batch(m.say(seg{"reading what to clean up…", roleAccent}), func() tea.Msg {
		plans := map[model.CloneKey]policy.Plan{}
		for _, k := range keys {
			p, err := eng.PlanCleanup(k, opts, cwd)
			if err != nil {
				if !bulk {
					return planMsg{seq: seq, owner: owner, err: err}
				}
				continue
			}
			if bulk {
				p.Remove = policy.WithoutForce(p.Remove)
			}
			if len(p.Remove) > 0 {
				plans[k] = p
			}
		}
		return planMsg{seq: seq, owner: owner, plans: plans, names: names, bulk: bulk}
	})
}

// planned shows the confirm of d or D.
func (m *Model) planned(msg planMsg) tea.Cmd {
	// Asked again since, another owner shown, or another popup open: a
	// confirm must be of what the user is looking at.
	if msg.seq != m.planSeq || msg.owner != m.owner || m.popup != nil {
		return nil
	}
	if msg.err != nil {
		return m.say(seg{"clean up: " + msg.err.Error(), roleWarn})
	}
	if len(msg.plans) == 0 {
		return m.say(seg{"nothing to clean up", roleMuted})
	}
	m.status = nil
	var body []drawn
	worktrees, branches := 0, 0
	for k, p := range msg.plans {
		if len(msg.plans) > 1 {
			body = append(body, drawn{parts: []cell{{text: msg.names[k], role: roleTitle, bold: true}}})
		}
		for _, it := range p.Remove {
			what := it.Branch
			if it.Kind == policy.ItemWorktree || it.Kind == policy.ItemPrunable {
				what = m.sym.Worktree + " " + m.short(it.Path)
				worktrees++
			} else {
				branches++
			}
			body = append(body, drawn{parts: []cell{{text: "  remove ", role: roleOK}, {text: what, role: roleText}, {text: "  " + it.Why, role: roleMuted}}})
		}
		if len(msg.plans) == 1 {
			for _, it := range p.Keep {
				what := it.Branch
				if it.Path != "" {
					what = m.sym.Worktree + " " + m.short(it.Path)
				}
				body = append(body, drawn{parts: []cell{{text: "  keep   ", role: rolePrompt}, {text: what, role: roleText}, {text: "  " + it.Why, role: roleMuted}}})
			}
		}
	}
	title := "clean up"
	for _, n := range msg.names {
		if len(msg.plans) == 1 && !msg.bulk {
			title = "clean up " + n
		}
	}
	if msg.bulk {
		title = "clean up all"
		body = append([]drawn{{parts: []cell{{text: fmt.Sprintf("%d worktrees and %d branches in %d clones; nothing not merged", worktrees, branches, len(msg.plans)), role: roleText}}}, {}}, body...)
	}
	body = append(body, drawn{}, drawn{parts: []cell{{text: "each clone is read again before anything goes; what changed since is left", role: roleMuted}}})
	opts := m.cleanupOpts(msg.bulk)
	plans := msg.plans
	return m.openConfirm(title, body, func(m *Model) tea.Cmd {
		return m.busySaid("clean up", m.eng.Cleanup(plans, opts, m.cwd))
	})
}

func (m *Model) openConfirm(title string, body []drawn, run func(*Model) tea.Cmd) tea.Cmd {
	m.open(m.confirmPopup(title, body, run))
	return nil
}

// vars are what an action's run and worktree_remove may use, for a row.
func vars(r *model.Row, tree *model.Worktree) map[string]string {
	c := r.Local.Value
	v := map[string]string{"path": c.Path, "branch": c.Head, "default": c.Default, "name": c.Name}
	if tree != nil {
		v["path"], v["branch"] = tree.Path, tree.Holds()
	}
	if c.Slug != "" {
		owner, name, _ := strings.Cut(c.Slug, "/")
		v["repo"], v["owner"], v["name"] = c.Slug, owner, name
		v["url"] = "https://" + c.Host + "/" + c.Slug
	}
	return v
}

// action runs the custom action i on the row selected: after a confirm
// when it asks for one, with the terminal when it is interactive.
func (m *Model) action(i int) tea.Cmd {
	a := m.actions[i]
	l, ok := m.selected()
	if !ok || l.row == nil || l.row.Clone == "" || !l.row.Local.Has {
		return m.say(seg{a.Name + ": select a clone", roleMuted})
	}
	var tree *model.Worktree
	if a.Scope == config.ScopeWorktree && l.kind == columns.KindWorktree {
		tree = l.tree
	}
	v := vars(l.row, tree)
	argv := config.Expand(a.Run, v)
	dir, clone := v["path"], string(l.row.Clone)
	name := a.Name
	if name == "" {
		name = argv[0]
	}
	run := func(m *Model) tea.Cmd {
		// The action may change the clone: it takes the clone's lane, as a
		// change of gh tidy's own does.
		if !m.eng.Hold(clone, name) {
			return m.say(seg{name + ": busy with another change", rolePrompt})
		}
		if a.Interactive {
			return tea.Exec(m.cmds.Interactive(dir, argv), func(err error) tea.Msg {
				return actionDoneMsg{name: name, path: clone, err: err}
			})
		}
		cmds := m.cmds
		return tea.Batch(m.say(seg{name + "…", roleAccent}), func() tea.Msg {
			out, err := cmds.Run(context.Background(), dir, argv)
			return actionDoneMsg{name: name, path: clone, out: out, err: err}
		})
	}
	if !a.Confirm {
		return run(m)
	}
	body := []drawn{
		{parts: []cell{{text: "run ", role: roleSubtle}, {text: quote(argv), role: roleStrong}}},
		{parts: []cell{{text: "in  ", role: roleSubtle}, {text: m.short(dir), role: roleMuted}}},
	}
	return m.openConfirm(name, body, run)
}

func (m *Model) actionDone(msg actionDoneMsg) tea.Cmd {
	m.eng.Release(msg.path)
	last := msg.out
	if i := strings.LastIndex(last, "\n"); i >= 0 {
		last = last[i+1:]
	}
	switch {
	case msg.err != nil:
		return m.say(seg{msg.name + ": " + msg.err.Error(), roleWarn})
	case last != "":
		return m.say(seg{msg.name + ": " + last, roleOK})
	}
	return m.say(seg{msg.name + ": done", roleOK})
}

// quote writes argv as a shell would read it back.
func quote(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = a
		if a == "" || strings.ContainsAny(a, " \t'\"$`\\") {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}
