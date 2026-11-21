package columns

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/model"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func ctx(counts bool) Ctx {
	s := config.Default().Symbols.Marks()
	s.Counts = counts
	return Ctx{Owner: &model.Owner{Login: "me"}, Symbols: s, Now: now}
}

// row is a clone of me/app on main, with origin, fetched an hour ago.
func row(f func(*model.Clone)) *model.Row {
	c := model.Clone{Name: "app", Path: "/src/me/app", OriginURL: "git@github.com:me/app", Default: "main",
		DefaultSource: model.DefaultFromGitHub, DefaultOid: "o1", FetchedAt: now.Add(-time.Hour),
		Status:    model.Status{Head: "main", Upstream: "origin/main", HasAB: true},
		Worktrees: []model.Worktree{{Path: "/src/me/app", Branch: "main", Main: true}},
		Branches:  []model.Branch{{Name: "main", Upstream: "origin/main"}}}
	if f != nil {
		f(&c)
	}
	r := &model.Row{Clone: "/src/me/app", Remote: "github.com/me/app", Origin: "github.com/me/app"}
	r.Local.Set(c, now)
	return r
}

func TestDefaultColumnsExist(t *testing.T) {
	d := config.Default()
	for lens, names := range map[Lens][]string{Local: d.Columns.Local, GitHub: d.Columns.GitHub} {
		if _, bad := Pick(lens, names); len(bad) > 0 {
			t.Errorf("%s: not columns: %v", lens, bad)
		}
	}
	seen := map[string]bool{}
	for _, c := range All {
		if seen[c.Name] {
			t.Errorf("%s twice", c.Name)
		}
		seen[c.Name] = true
	}
}

func TestMainCell(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts bool
		f      func(*model.Clone)
		ahead  bool
		want   string
	}{
		{"up to date", true, nil, false, "="},
		{"behind", true, func(c *model.Clone) { c.Branches[0].Behind = 6 }, false, "⇣6"},
		{"behind, no counts", false, func(c *model.Clone) { c.Branches[0].Behind = 6 }, false, "⇣"},
		{"GitHub ahead only", true, nil, true, "⇣?"},
		{"behind and GitHub ahead", true, func(c *model.Clone) { c.Branches[0].Behind = 2 }, true, "⇣2?"},
		{"diverged", true, func(c *model.Clone) { c.Branches[0].Behind, c.Branches[0].Ahead = 3, 1 }, false, "⇣3⇡1"},
		{"diverged, no counts", false, func(c *model.Clone) { c.Branches[0].Behind, c.Branches[0].Ahead = 3, 1 }, false, "⇕"},
		{"no origin", true, func(c *model.Clone) { c.OriginURL = "" }, false, "no origin"},
		{"no local main", true, func(c *model.Clone) { c.Branches = nil }, false, "no main"},
		{"tracks upstream", true, func(c *model.Clone) { c.Branches[0].Upstream = "upstream/main" }, false, "tracks upstream/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := row(tc.f)
			if tc.ahead {
				r.Ahead.Set(model.Ahead{Remote: "o2", Local: "o1", Ahead: true}, now)
			}
			if got := text(mainCell(ctx(tc.counts), Item{Kind: KindClone, Row: r})); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	// GitHub being ahead of an older origin is not said of the new one.
	r := row(nil)
	r.Ahead.Set(model.Ahead{Remote: "o2", Local: "o0", Ahead: true}, now)
	if got := text(mainCell(ctx(true), Item{Kind: KindClone, Row: r})); got != "=" {
		t.Errorf("stale ahead: %q", got)
	}
	r.Op = model.Op{Doing: "fetching"}
	if got := text(mainCell(ctx(true), Item{Kind: KindClone, Row: r})); got != "fetching" {
		t.Errorf("op: %q", got)
	}
}

func TestCanUpdate(t *testing.T) {
	r := row(func(c *model.Clone) { c.Branches[0].Behind = 2; c.Worktrees[0].Dirty, c.Worktrees[0].Tracked = 1, 1 })
	if s := Main(r); s.CanUpdate() || !strings.Contains(s.Blocked, "uncommitted") {
		t.Errorf("dirty main: %+v", s)
	}
	// Untracked files do not keep a fast-forward out.
	r = row(func(c *model.Clone) { c.Branches[0].Behind = 2; c.Worktrees[0].Dirty = 2 })
	if s := Main(r); !s.CanUpdate() {
		t.Errorf("untracked only: %+v", s)
	}
	// Two worktrees holding main, or one that cannot be read, keep it out.
	r = row(func(c *model.Clone) {
		c.Branches[0].Behind = 2
		c.Worktrees = append(c.Worktrees, model.Worktree{Path: "/wt/m", Branch: "main"})
	})
	if Main(r).CanUpdate() {
		t.Error("main held twice updates")
	}
	r = row(func(c *model.Clone) { c.Branches[0].Behind = 2; c.Worktrees[0].Tracked = -1 })
	if Main(r).CanUpdate() {
		t.Error("an unreadable holder updates")
	}
	r = row(func(c *model.Clone) { c.Branches[0].Behind = 2; c.DefaultSource = model.DefaultFromGuess })
	if Main(r).CanUpdate() {
		t.Error("a guessed default updates")
	}
	r = row(func(c *model.Clone) { c.Branches[0].Behind = 2 })
	if !Main(r).CanUpdate() {
		t.Error("behind does not update")
	}
}

func TestCheckedOutAndChanges(t *testing.T) {
	r := row(func(c *model.Clone) {
		c.Head, c.Upstream, c.Ahead, c.BaseBehind = "fix", "origin/fix", 2, 5
		c.Branches = append(c.Branches, model.Branch{Name: "fix", Upstream: "origin/fix", Ahead: 2})
		c.Unstaged, c.Staged, c.Untracked, c.Stash = 1, 2, 0, 1
	})
	it := Item{Kind: KindClone, Row: r}
	if got := text(checkedOutCell(ctx(true), it)); got != "fix ⇡2 ⇠5" {
		t.Errorf("checked out: %q", got)
	}
	if got := text(changesCell(ctx(true), it)); got != "!1 +2 $1" {
		t.Errorf("changes: %q", got)
	}
	if got := text(changesCell(ctx(false), it)); got != "!+$" {
		t.Errorf("changes without counts: %q", got)
	}
	if got := text(checkedOutCell(ctx(true), Item{Kind: KindClone, Row: row(nil)})); got != "main" {
		t.Errorf("on main: %q", got)
	}
}

func TestWorktrees(t *testing.T) {
	r := row(func(c *model.Clone) {
		c.Worktrees = append(c.Worktrees,
			model.Worktree{Path: "/wt/a", Branch: "a", Active: now.Add(-2 * time.Hour)},
			model.Worktree{Path: "/wt/b", Branch: "b", Created: now.Add(-time.Hour)},
			model.Worktree{Path: "/wt/c", Branch: "c", Dirty: 2},
			model.Worktree{Path: "/wt/gone", Branch: "g", Prunable: true})
		c.Branches = append(c.Branches, model.Branch{Name: "a"}, model.Branch{Name: "b"}, model.Branch{Name: "c"},
			model.Branch{Name: "g"}, model.Branch{Name: "loose"})
	})
	it := Item{Kind: KindClone, Row: r}
	if got := text(worktreesCell(ctx(true), it)); got != "1 done  3 ⋅  · +1 br" {
		t.Errorf("before facts: %q", got)
	}
	r.Facts.Set(model.CleanupFacts{Merged: map[string]bool{"main": true, "a": true, "b": true},
		WorkedOn: map[string]bool{"a": true, "c": true}}, now)
	if got := text(worktreesCell(ctx(true), it)); got != "1 busy  1 new  2 done  · +1 br" {
		t.Errorf("verdicts: %q", got)
	}
	a := r.Local.Value.Worktrees[1]
	if got := text(worktreesCell(ctx(true), Item{Kind: KindWorktree, Row: r, Tree: &a})); got != "done 2h" {
		t.Errorf("worktree row: %q", got)
	}
	if got := text(repoCell(ctx(true), it)); got != "app ⌥4" {
		t.Errorf("repo: %q", got)
	}
}

func TestRepoCell(t *testing.T) {
	r := row(nil)
	r.Origin = "github.com/other/app"
	if got := text(repoCell(ctx(true), Item{Kind: KindClone, Row: r})); got != "app → other/app" {
		t.Errorf("other owner: %q", got)
	}
	all := ctx(true)
	all.Owner = &model.Owner{All: true}
	if got := text(repoCell(all, Item{Kind: KindClone, Row: r})); got != "other/app" {
		t.Errorf("all: %q", got)
	}
	nc := &model.Row{Remote: "github.com/me/tool"}
	nc.Listed.Set(model.Remote{Name: "tool", Fork: true}, now)
	if got := text(repoCell(ctx(true), Item{Kind: KindRemote, Row: nc})); got != "tool fork" {
		t.Errorf("not cloned: %q", got)
	}
}

func TestGitHubCells(t *testing.T) {
	r := row(nil)
	it := Item{Kind: KindClone, Row: r}
	if got := text(issuesCell(ctx(true), it)); got != "⋅" {
		t.Errorf("unread: %q", got)
	}
	r.Heavy.Set(model.Heavy{Found: true, Issues: 3, Alerts: 2, CI: "FAILURE", Fields: model.NeedCounts | model.NeedCI}, now)
	// Read without the creation times: which are new is not known.
	if got := text(issuesCell(ctx(true), it)); got != "3 ⋅" {
		t.Errorf("issues: %q", got)
	}
	r.New = model.New{Known: true, Issues: 1}
	if got := text(issuesCell(ctx(true), it)); got != "3 +1" {
		t.Errorf("one new: %q", got)
	}
	r.New = model.New{Known: true, Issues: 5, MoreIssues: true}
	if got := text(issuesCell(ctx(true), it)); got != "3 +5+" {
		t.Errorf("more new than read: %q", got)
	}
	r.New = model.New{Known: true}
	if got := text(issuesCell(ctx(true), it)); got != "3" {
		t.Errorf("none new: %q", got)
	}
	if got := text(dependabotCell(ctx(true), it)); got != "⋅" {
		t.Errorf("alerts not asked for: %q", got)
	}
	if got := text(ciCell(ctx(true), it)); got != "✗" {
		t.Errorf("ci: %q", got)
	}
	r.Heavy.Value.Fields |= model.NeedAlerts
	r.Heavy.Value.AlertsOff = true
	if got := text(dependabotCell(ctx(true), it)); got != "off" {
		t.Errorf("alerts off: %q", got)
	}
	denied := &model.Row{Remote: "github.com/me/x"}
	denied.Heavy.Deny("SSO", now)
	if got := text(prsCell(ctx(true), Item{Kind: KindClone, Row: denied})); got != "n/a" {
		t.Errorf("denied: %q", got)
	}
}

func TestFit(t *testing.T) {
	cs, _ := Pick(Local, config.Default().Columns.Local)
	got, ws := Fit(cs, 200)
	if len(got) != len(cs) || slices.Contains(ws, 0) {
		t.Fatalf("wide: %d columns, widths %v", len(got), ws)
	}
	got, _ = Fit(cs, 60)
	names := []string{}
	for _, c := range got {
		names = append(names, c.Name)
	}
	if !slices.Contains(names, "repo") || !slices.Contains(names, "main") || slices.Contains(names, "fetched") {
		t.Errorf("narrow: %v", names)
	}
	if n := Needs(GitHub, []Column{{Need: model.NeedCI}}); !n.Has(model.NeedNotCloned) {
		t.Error("the github lens reads repositories not cloned")
	}
}

func TestSections(t *testing.T) {
	if _, bad := PickSections(config.Default().Preview.Sections); len(bad) > 0 {
		t.Errorf("not sections: %v", bad)
	}
	r := row(func(c *model.Clone) {
		c.Branches[0].Behind = 3
		c.Head, c.BaseBehind, c.Worktrees[0].Branch = "fix", 2, "fix"
		c.Branches = append(c.Branches, model.Branch{Name: "fix"})
		c.Unstaged, c.Untracked = 1, 1
	})
	it := Item{Kind: KindClone, Row: r}
	d := &model.Detail{Incoming: []model.Commit{{Hash: "a1", Subject: "one"}}, Unpushed: []model.Commit{{Hash: "b1", Subject: "mine"}},
		NeverPushed: true, Files: []model.File{{XY: " M", Path: "f"}, {XY: "??", Path: "n"}}}
	var heads []string
	for _, s := range Sections {
		if p := s.Build(ctx(true), it, d); p != nil {
			heads = append(heads, text(p.Head))
		}
	}
	want := []string{
		"⇣3  on origin/main, not in your main · p brings them in",
		"NEVER PUSHED  fix exists only on this Mac",
		"⇠2  on main, not in fix",
		"!1 ?1  uncommitted, on fix",
	}
	if !slices.Equal(heads, want) {
		t.Errorf("heads:\n got %q\nwant %q", heads, want)
	}
	p := incoming(ctx(true), it, d)
	if got := text(p.Lines[0]); got != "  a1 one" || text(p.Lines[1]) != "  … 2 more" {
		t.Errorf("incoming lines: %q", got)
	}
	if p := incoming(ctx(true), it, nil); text(p.Lines[0]) != "  reading…" {
		t.Error("no reading line while the detail is read")
	}
	var labels []string
	for _, f := range Facts(ctx(true), it, "/src") {
		labels = append(labels, f.Label+" "+text(f.Value))
	}
	if !slices.Contains(labels, "path ~/me/app") || !slices.Contains(labels, "p updates main · ff, not checked out") {
		t.Errorf("facts: %q", labels)
	}
}

// The stars come from the list, or asked by name for an owner whose list
// is not read; a count never read is not shown as 0.
func TestStars(t *testing.T) {
	text := func(r model.Row) string {
		var b strings.Builder
		for _, s := range starsCell(ctx(true), Item{Row: &r, Kind: KindClone}) {
			b.WriteString(s.Text)
		}
		return b.String()
	}
	listed := model.Row{Remote: "github.com/me/app"}
	listed.Listed.Set(model.Remote{Stars: 12345}, now)
	asked := model.Row{Remote: "github.com/other/lib"}
	asked.Heavy.Set(model.Heavy{Found: true, Stars: 7}, now)
	unread := model.Row{Remote: "github.com/other/new"}
	for _, tt := range []struct {
		row  model.Row
		want string
	}{{listed, "12.3k"}, {asked, "7"}, {unread, Unread[0].Text}, {model.Row{}, "-"}} {
		if got := text(tt.row); got != tt.want {
			t.Errorf("stars of %s = %q; want %q", tt.row.Remote, got, tt.want)
		}
	}
}

// A clone whose owner and name another clone shares, as in two roots,
// says which root it is in; other clones do not.
func TestTwinsSayTheirRoot(t *testing.T) {
	roots := []string{"/home/me/src", "/home/me/work", "/mnt/b/src"}
	row := func(path string) model.Row {
		r := model.Row{Clone: model.NewCloneKey(path)}
		r.Local.Set(model.Clone{Path: path, Name: filepath.Base(path)}, now)
		return r
	}
	rows := []model.Row{row("/home/me/src/github.com/acme/api"), row("/home/me/work/github.com/acme/api"),
		row("/mnt/b/src/github.com/acme/api"), row("/home/me/src/github.com/acme/web")}
	x := ctx(true)
	x.Roots, x.Twins = roots, Twins(rows)
	text := func(r model.Row) string {
		var b strings.Builder
		for _, s := range repoCell(x, Item{Row: &r, Kind: KindClone}) {
			b.WriteString(s.Text)
		}
		return b.String()
	}
	for i, want := range []string{"api me/src", "api work", "api b/src", "web"} {
		if got := text(rows[i]); !strings.HasPrefix(got, want) || (i == 3 && strings.Contains(got, "src")) {
			t.Errorf("repo of %s = %q; want %q", rows[i].Clone, got, want)
		}
	}
}

func TestShortWritesHomeAsTilde(t *testing.T) {
	x := Ctx{Home: "/home/me"}
	for in, want := range map[string]string{"/home/me/src/a": "~/src/a", "/home/meow/a": "/home/meow/a", "/tmp/a": "/tmp/a"} {
		if got := x.Short(in); got != want {
			t.Errorf("Short(%s) = %s; want %s", in, got, want)
		}
	}
}
