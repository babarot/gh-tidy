package columns

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/babarot/gh-tidy/internal/model"
)

// Section is one part of the preview pane's left side: what is behind one
// of the row's marks, headed by the same mark.
type Section struct {
	Name  string
	Kinds []Kind
	// Detail says the section reads the row's detail (its commits, files
	// and stash), which is read for the row selected only.
	Detail bool
	// Build writes the section of an item; nil when it has nothing to
	// show. d is nil while the detail is being read.
	Build func(x Ctx, it Item, d *model.Detail) *Part
}

// Part is a section written: its head, and its lines.
type Part struct {
	Name  string
	Head  Cell
	Lines []Cell
}

// Sections are every section the config file may name, in no order.
var Sections = []Section{
	{"incoming", []Kind{KindClone}, true, incoming},
	{"unpushed", []Kind{KindClone, KindWorktree}, true, unpushed},
	{"behind_main", []Kind{KindClone, KindWorktree}, true, behindMain},
	{"changes", []Kind{KindClone, KindWorktree}, true, changedFiles},
	{"stash", []Kind{KindClone}, true, stashed},
	{"worktrees", []Kind{KindClone}, false, worktreeList},
}

// PickSections returns the sections named, or the names that are not
// sections.
func PickSections(names []string) ([]Section, []string) {
	var ss []Section
	var bad []string
	for _, n := range names {
		i := slices.IndexFunc(Sections, func(s Section) bool { return s.Name == n })
		if i < 0 {
			bad = append(bad, n)
			continue
		}
		ss = append(ss, Sections[i])
	}
	return ss, bad
}

// SectionNames are the names of every section, for a message.
func SectionNames() []string {
	ns := make([]string, len(Sections))
	for i, s := range Sections {
		ns[i] = s.Name
	}
	return ns
}

func head(mark string, role Role, words string) Cell {
	c := Cell{{Text: mark, Role: role, Bold: true}}
	if words != "" {
		c = append(c, Seg{Text: "  " + words, Role: Subtle})
	}
	return c
}

func reading() []Cell { return []Cell{{{Text: "  reading…", Role: Dim}}} }

func commitLines(x Ctx, cs []model.Commit, total int) []Cell {
	out := make([]Cell, 0, len(cs)+1)
	for _, c := range cs {
		line := Cell{{Text: "  " + c.Hash + " ", Role: Dim}, {Text: c.Subject, Role: Text}}
		meta := c.Author
		if a := Age(c.When, x.Now); a != "" {
			meta += " " + a
		}
		if meta != "" {
			line = append(line, Seg{Text: "  " + meta, Role: Dim})
		}
		out = append(out, line)
	}
	if total > len(cs) {
		out = append(out, Cell{{Text: fmt.Sprintf("  … %d more", total-len(cs)), Role: Dim}})
	}
	return out
}

func incoming(x Ctx, it Item, d *model.Detail) *Part {
	s := Main(it.Row)
	def := s.Name
	switch s.Kind {
	case MainBehind, MainDiverged:
		words := "on origin/" + def + ", not in your " + def
		if s.CanUpdate() {
			words += " · p brings them in"
		} else if s.Blocked != "" {
			words += " · p leaves them: " + s.Blocked
		}
		p := &Part{Head: head(x.sym(x.Symbols.Behind, s.Behind), Accent, words)}
		if d == nil {
			p.Lines = reading()
		} else {
			p.Lines = commitLines(x, d.Incoming, s.Behind)
		}
		if s.Kind == MainDiverged {
			p.Lines = append(p.Lines, Cell{{Text: fmt.Sprintf("  your %s also has %d commit%s origin/%s lacks: p cannot fast-forward it", def, s.Ahead, plural(s.Ahead), def), Role: Warn}})
		}
		return p
	case MainAhead:
		return &Part{Head: head(x.sym(x.Symbols.Ahead, s.Ahead), Warn, "your "+def+" has commits origin/"+def+" lacks"),
			Lines: []Cell{{{Text: "  p cannot fast-forward it; push or reset it yourself", Role: Muted}}}}
	}
	if s.GitHubAhead {
		return &Part{Head: head(x.Symbols.GitHubAhead, Prompt, "GitHub has commits newer than your last fetch"),
			Lines: []Cell{{{Text: "  what they are is known after a fetch: f fetches this clone, P fetches every clone", Role: Muted}}}}
	}
	return nil
}

// branchOfItem is the branch an item stands for, and the clone.
func branchOfItem(it Item) (string, model.Clone) {
	c := it.Row.Local.Value
	if it.Kind == KindWorktree {
		return it.Tree.Holds(), c
	}
	return c.Head, c
}

func unpushed(x Ctx, it Item, d *model.Detail) *Part {
	if !it.Row.Local.Has {
		return nil
	}
	name, c := branchOfItem(it)
	b, ok := branchOf(c, name)
	if !ok || name == "" {
		return nil
	}
	switch {
	case b.Upstream != "" && !b.Gone && b.Ahead > 0:
		p := &Part{Head: head(x.sym(x.Symbols.Ahead, b.Ahead), Worktree, "on "+name+", not pushed")}
		if d == nil {
			p.Lines = reading()
		} else {
			p.Lines = commitLines(x, d.Unpushed, b.Ahead)
		}
		return p
	case (b.Upstream == "" || b.Gone) && name != c.Default && c.HasOrigin():
		words := name + " exists only on this Mac"
		if b.Gone {
			words = name + "'s upstream was deleted"
		}
		p := &Part{Head: head("NEVER PUSHED", Worktree, words)}
		if b.Gone {
			p.Head = head("UPSTREAM GONE", Warn, words)
		}
		if d == nil {
			p.Lines = reading()
		} else {
			p.Lines = commitLines(x, d.Unpushed, len(d.Unpushed))
		}
		return p
	}
	return nil
}

func behindMain(x Ctx, it Item, d *model.Detail) *Part {
	if !it.Row.Local.Has {
		return nil
	}
	name, c := branchOfItem(it)
	n := c.BaseBehind
	if it.Kind == KindWorktree {
		n = 0
		if it.Tree.Measured {
			n = it.Tree.BehindMain
		}
	}
	if n == 0 || name == "" || name == c.Default {
		return nil
	}
	p := &Part{Head: head(x.sym(x.Symbols.BehindMain, n), Accent, "on "+c.Default+", not in "+name)}
	if d == nil {
		p.Lines = reading()
	} else {
		p.Lines = commitLines(x, d.BehindMain, n)
	}
	return p
}

func changedFiles(x Ctx, it Item, d *model.Detail) *Part {
	if !it.Row.Local.Has {
		return nil
	}
	name, c := branchOfItem(it)
	var mark string
	if it.Kind == KindWorktree {
		if it.Tree.Dirty <= 0 {
			return nil
		}
		mark = x.sym(x.Symbols.Unstaged, it.Tree.Dirty)
	} else {
		st := c.Status
		st.Stash = 0
		if st.Dirty()+st.Untracked == 0 {
			return nil
		}
		mark = text(x.changes(st))
	}
	words := "uncommitted"
	if name != "" {
		words += ", on " + name
	}
	p := &Part{Head: head(mark, Prompt, words)}
	if d == nil {
		p.Lines = reading()
		return p
	}
	for _, f := range d.Files {
		role := Prompt
		switch {
		case f.XY == "??":
			role = Muted
		case f.XY[1] == ' ':
			role = OK // staged only
		}
		p.Lines = append(p.Lines, Cell{{Text: "  " + f.XY + " ", Role: role}, {Text: f.Path, Role: Text}})
	}
	return p
}

func stashed(x Ctx, it Item, d *model.Detail) *Part {
	if !it.Row.Local.Has || it.Row.Local.Value.Stash == 0 {
		return nil
	}
	n := it.Row.Local.Value.Stash
	p := &Part{Head: head(x.sym(x.Symbols.Stash, n), Title, "stashed")}
	if d == nil {
		p.Lines = reading()
		return p
	}
	for _, s := range d.Stash {
		line := Cell{{Text: "  " + s.Ref + "  ", Role: Dim}, {Text: s.Subject, Role: Text}}
		if a := Age(s.When, x.Now); a != "" {
			line = append(line, Seg{Text: "  " + a, Role: Dim})
		}
		p.Lines = append(p.Lines, line)
	}
	return p
}

func worktreeList(x Ctx, it Item, _ *model.Detail) *Part {
	r := it.Row
	if !r.Local.Has {
		return nil
	}
	linked := r.Local.Value.Linked()
	loose := LooseBranches(r)
	if len(linked) == 0 && len(loose) == 0 {
		return nil
	}
	var p *Part
	if len(linked) > 0 {
		p = &Part{Head: head(x.Symbols.Worktree+strconv.Itoa(len(linked)), Worktree, "worktrees · enter shows them as rows")}
		for _, w := range linked {
			name := w.Holds()
			if name == "" {
				name = "(detached)"
			}
			role := Dim
			if w.Prunable {
				role = Warn
			}
			line := Cell{{Text: "  " + name + "  ", Role: Worktree}, {Text: w.Path + "  ", Role: role}}
			p.Lines = append(p.Lines, append(line, x.worktreeVerdict(r, w)...))
		}
	} else {
		p = &Part{Head: head(fmt.Sprintf("+%d br", len(loose)), Muted, "branches in no worktree, with work found nowhere else")}
	}
	if len(linked) > 0 && len(loose) > 0 {
		p.Lines = append(p.Lines, Cell{{Text: fmt.Sprintf("  +%d br  in no worktree, with work found nowhere else", len(loose)), Role: Muted}})
	}
	for _, b := range loose {
		why, role := "never pushed", Worktree
		if b.Gone {
			why, role = "upstream deleted, not merged", Warn
		}
		p.Lines = append(p.Lines, Cell{{Text: "  " + b.Name + "  ", Role: Strong}, {Text: why, Role: role}})
	}
	return p
}

// Fact is one line of the preview's right side: a label and its value.
type Fact struct {
	Label string
	Value Cell
}

// Facts are what does not change as the pane scrolls: where the row is,
// when it was fetched, and what p and d would do to it now.
func Facts(x Ctx, it Item, home string) []Fact {
	r := it.Row
	short := func(p string) string {
		if home != "" && strings.HasPrefix(p, home+"/") {
			return "~" + p[len(home):]
		}
		return p
	}
	switch it.Kind {
	case KindHead:
		return nil
	case KindRemote:
		fs := []Fact{{"repo", Cell{{Text: slugOf(r.Remote), Role: Text}}}}
		if r.Listed.Has {
			fs = append(fs, Fact{"pushed", Cell{{Text: Ago(r.Listed.Value.PushedAt, x.Now), Role: Muted}}})
		}
		return append(fs, Fact{"c", Cell{{Text: "clones it", Role: Muted}}}, Fact{"o", Cell{{Text: "opens it on GitHub", Role: Muted}}})
	case KindWorktree:
		w := *it.Tree
		role := Text
		if w.Prunable {
			role = Warn
		}
		fs := []Fact{{"path", Cell{{Text: short(w.Path), Role: role}}}, {"clone", Cell{{Text: short(string(r.Clone)), Role: Muted}}}}
		if !w.Created.IsZero() {
			fs = append(fs, Fact{"made", Cell{{Text: Ago(w.Created, x.Now), Role: Text}}})
		}
		if !w.Active.IsZero() {
			fs = append(fs, Fact{"touched", Cell{{Text: Ago(w.Active, x.Now), Role: Text}}})
		}
		if v, _, ok := Verdict(r, w, x.Now); ok {
			if Removes(v) {
				fs = append(fs, Fact{"d", Cell{{Text: "removes it", Role: OK}, {Text: " · " + string(v), Role: Muted}}})
			} else {
				fs = append(fs, Fact{"d", Cell{{Text: "keeps it", Role: Prompt}, {Text: " · " + string(v), Role: Muted}}})
			}
		}
		return fs
	}
	fs := []Fact{{"path", Cell{{Text: short(string(r.Clone)), Role: Text}}}}
	if !r.Local.Has {
		return fs
	}
	c := r.Local.Value
	if c.HasOrigin() {
		f := Cell{{Text: Ago(c.FetchedAt, x.Now), Role: Muted}}
		if GitHubAhead(r) {
			f = Cell{{Text: Ago(c.FetchedAt, x.Now), Role: Prompt}, {Text: " · GitHub is ahead", Role: Prompt}}
		}
		fs = append(fs, Fact{"fetched", f})
	}
	fs = append(fs, Fact{"p", pFact(Main(r))})
	fs = append(fs, Fact{"d", dFact(x, r)})
	if c.Default != "" {
		fs = append(fs, Fact{"default", Cell{{Text: c.Default, Role: Strong}, {Text: " from " + string(c.DefaultSource), Role: Dim}}})
	}
	return fs
}

func pFact(s MainState) Cell {
	switch {
	case s.CanUpdate() && s.CheckedOut:
		return Cell{{Text: "updates " + s.Name, Role: OK}, {Text: " · ff where it is checked out", Role: Muted}}
	case s.CanUpdate():
		return Cell{{Text: "updates " + s.Name, Role: OK}, {Text: " · ff, not checked out", Role: Muted}}
	case s.Blocked != "" && (s.Kind == MainBehind || s.GitHubAhead):
		return Cell{{Text: "skips it: " + s.Blocked, Role: Prompt}}
	}
	switch s.Kind {
	case MainCurrent:
		return Cell{{Text: "leaves it: " + s.Name + " is up to date", Role: Muted}}
	case MainNoOrigin:
		return Cell{{Text: "leaves it: no origin", Role: Muted}}
	case MainAhead, MainDiverged:
		return Cell{{Text: "skips it: not a fast-forward", Role: Prompt}}
	case MainNoLocal:
		return Cell{{Text: "skips it: no local " + s.Name, Role: Prompt}}
	case MainOtherUpstream:
		return Cell{{Text: "skips it: " + s.Name + " does not track origin", Role: Prompt}}
	}
	return Cell{{Text: "skips it: the default branch is not known", Role: Prompt}}
}

func dFact(x Ctx, r *model.Row) Cell {
	linked := r.Local.Value.Linked()
	loose := len(LooseBranches(r))
	if len(linked) == 0 && loose == 0 {
		return Cell{{Text: "nothing to clean up", Role: Muted}}
	}
	rm, keep, unread := 0, 0, 0
	for _, w := range linked {
		v, _, ok := Verdict(r, w, x.Now)
		switch {
		case !ok:
			unread++
		case Removes(v):
			rm++
		default:
			keep++
		}
	}
	if unread > 0 {
		return Cell{{Text: "decides when pressed", Role: Muted}}
	}
	role := Muted
	if rm > 0 {
		role = OK
	}
	c := Cell{{Text: fmt.Sprintf("removes %d worktree%s", rm, plural(rm)), Role: role}}
	kept := []string{}
	if keep > 0 {
		kept = append(kept, fmt.Sprintf("%d worktree%s", keep, plural(keep)))
	}
	if loose > 0 {
		kept = append(kept, fmt.Sprintf("%d branch%s", loose, map[bool]string{true: "", false: "es"}[loose == 1]))
	}
	if len(kept) > 0 {
		c = append(c, Seg{Text: " · keeps " + strings.Join(kept, ", "), Role: Muted})
	}
	return c
}

// detailLines is how many alerts or checks the pane lists; the rest are
// counted.
const detailLines = 8

// GitHubParts are what the github set's pane shows of a repository: what
// the heavy fields say, and under them the alerts and checks of d, read
// for the repository selected.
func GitHubParts(x Ctx, it Item, d model.GitHubDetail) []Part {
	r := it.Row
	switch {
	case it.Kind == KindHead || it.Kind == KindWorktree:
		return nil
	case r.Remote == "":
		return []Part{{Head: head("GITHUB", Subtle, "no repository on GitHub")}}
	case !r.Heavy.Has && r.Heavy.Last == model.Denied:
		return []Part{{Head: head("NOT READABLE", Warn, ""), Lines: []Cell{{{Text: "  " + r.Heavy.Why, Role: Muted}}}}}
	case !r.Heavy.Has:
		return []Part{{Head: head("GITHUB", Subtle, "not read yet")}}
	}
	h := r.Heavy.Value
	var ps []Part
	if r.Listed.Has && r.Listed.Value.Description != "" {
		ps = append(ps, Part{Head: head("ABOUT", Subtle, ""), Lines: []Cell{{{Text: "  " + r.Listed.Value.Description, Role: Text}}}})
	}
	if h.Fields.Has(model.NeedCounts) {
		ps = append(ps, Part{Head: head(fmt.Sprintf("OPEN %d issues · %d pull requests", h.Issues, h.PRs), Subtle, ""), Lines: openLines(x, r.New, d)})
	}
	if h.Fields.Has(model.NeedAlerts) {
		switch {
		case h.AlertsDenied:
			ps = append(ps, Part{Head: head("DEPENDABOT n/a", Dim, ""), Lines: []Cell{{{Text: "  this token may not read them: they are for write, maintain and admin access, or whom an admin gives them to", Role: Muted}}}})
		case h.AlertsOff:
			ps = append(ps, Part{Head: head("DEPENDABOT off", Muted, ""), Lines: []Cell{{{Text: "  Dependabot alerts are not turned on for this repository, so nothing is checked", Role: Muted}}}})
		case h.Alerts > 0:
			ps = append(ps, Part{Head: head(fmt.Sprintf("DEPENDABOT %d", h.Alerts), Warn, "open alerts · o opens the repository"), Lines: alertLines(d)})
		}
	}
	if h.Fields.Has(model.NeedCI) {
		switch h.CI {
		case "FAILURE", "ERROR":
			ps = append(ps, Part{Head: head("CI ✗", Warn, "on "+h.Default), Lines: checkLines(d, false)})
		case "PENDING", "EXPECTED":
			ps = append(ps, Part{Head: head("CI ~", Prompt, "on "+h.Default+", running"), Lines: checkLines(d, true)})
		}
	}
	return ps
}

// openLines list the open issues and pull requests, the newest first, a
// new one marked: made after the time seen, as the counts tell them.
func openLines(x Ctx, n model.New, d model.GitHubDetail) []Cell {
	if c, ok := detailState(d); !ok {
		if c == nil {
			return nil
		}
		if d.State == model.DetailAll {
			c = Cell{{Text: "  open the owner to read the titles: all reads no more of GitHub", Role: Dim}}
		}
		return []Cell{c}
	}
	v := d.Value
	if !v.TitlesAsked {
		return nil
	}
	type item struct {
		o  model.Opened
		pr bool
	}
	var items []item
	for _, o := range v.Issues {
		items = append(items, item{o, false})
	}
	for _, o := range v.PRs {
		items = append(items, item{o, true})
	}
	slices.SortStableFunc(items, func(a, b item) int { return b.o.Created.Compare(a.o.Created) })
	var out []Cell
	for _, it := range items {
		mark := Seg{Text: "    "}
		if n.Known && it.o.Created.After(n.Since) {
			mark = Seg{Text: "  • ", Role: Accent}
		}
		kind := Seg{Text: "issue ", Role: Dim}
		if it.pr {
			kind = Seg{Text: "pr    ", Role: Dim}
		}
		out = append(out, Cell{mark, kind, {Text: fmt.Sprintf("#%d ", it.o.Number), Role: Muted}, {Text: it.o.Title, Role: Text},
			{Text: "  " + it.o.Author + " " + Ago(it.o.Created, x.Now), Role: Dim}})
	}
	switch {
	case !v.IssuesRead && !v.PRsRead:
		out = append(out, Cell{{Text: "  not read: GitHub did not list them", Role: Muted}})
	case !v.IssuesRead:
		out = append(out, Cell{{Text: "  the issues were not read", Role: Muted}})
	case !v.PRsRead:
		out = append(out, Cell{{Text: "  the pull requests were not read", Role: Muted}})
	}
	return out
}

// detailState says where the read of d stands, when it is not read; ok
// is true when it is.
func detailState(d model.GitHubDetail) (Cell, bool) {
	switch d.State {
	case model.DetailRead:
		return nil, true
	case model.DetailReading:
		return Cell{{Text: "  reading…", Role: Dim}}, false
	case model.DetailHeld:
		return Cell{{Text: "  not read: " + d.Why, Role: Muted}}, false
	case model.DetailFailed:
		return Cell{{Text: "  not read: " + d.Why + ", tried again in a while", Role: Muted}}, false
	case model.DetailAll:
		return Cell{{Text: "  open the owner to read them: all reads no more of GitHub", Role: Dim}}, false
	}
	return nil, false
}

// severityRole colors an alert by how severe it is.
var severityRole = map[string]Role{"critical": Warn, "high": Warn, "moderate": Prompt, "medium": Prompt, "low": Muted}

// alertLines list the open alerts, the most severe first.
func alertLines(d model.GitHubDetail) []Cell {
	if c, ok := detailState(d); !ok {
		if c == nil {
			return nil
		}
		return []Cell{c}
	}
	v := d.Value
	switch {
	case !v.AlertsAsked:
		return nil
	case v.AlertsDenied:
		return []Cell{{{Text: "  this token may not read them", Role: Muted}}}
	case len(v.Alerts) == 0:
		return []Cell{{{Text: "  GitHub listed none: this token may not read them", Role: Muted}}}
	}
	shown := v.Alerts[:min(len(v.Alerts), detailLines)]
	fixedText := func(a model.Alert) string {
		if a.Fixed == "" {
			return "no fix yet"
		}
		return "fixed in " + a.Fixed
	}
	pw, rw, fw := 0, 0, 0
	for _, a := range shown {
		pw = max(pw, len([]rune(a.Package+" ("+a.Ecosystem+")")))
		rw = max(rw, len([]rune(a.Range)))
		fw = max(fw, len([]rune(fixedText(a))))
	}
	var out []Cell
	for _, a := range shown {
		ft := fixedText(a)
		fixed := Seg{Text: ft + strings.Repeat(" ", fw-len([]rune(ft))), Role: Muted}
		if a.Fixed != "" {
			fixed.Role = OK
		}
		pkg := a.Package + " (" + a.Ecosystem + ")"
		out = append(out, Cell{{Text: "  "}, {Text: fmt.Sprintf("%-8s", a.Severity), Role: severityRole[a.Severity]},
			{Text: "  " + pkg + strings.Repeat(" ", pw-len([]rune(pkg))), Role: Text},
			{Text: "  " + a.Range + strings.Repeat(" ", rw-len([]rune(a.Range))) + "  ", Role: Muted}, fixed,
			{Text: "  " + a.Manifest, Role: Dim}})
	}
	if more := max(v.AlertsTotal, len(v.Alerts)) - len(shown); more > 0 {
		out = append(out, Cell{{Text: fmt.Sprintf("  … %d more", more), Role: Dim}})
	}
	return out
}

// checkLines list the checks that failed or run; pending says the commit's
// checks are running, not failed.
func checkLines(d model.GitHubDetail, pending bool) []Cell {
	if c, ok := detailState(d); !ok {
		if c == nil {
			return nil
		}
		return []Cell{c}
	}
	v := d.Value
	switch {
	case !v.ChecksAsked:
		return nil
	case v.ChecksErr != "":
		return []Cell{{{Text: "  not read: GitHub could not read the commit", Role: Muted}}}
	case len(v.Checks) == 0 && v.ChecksFailed+v.ChecksRunning > 0:
		return []Cell{{{Text: fmt.Sprintf("  %d failed · %d running, beyond the checks read", v.ChecksFailed, v.ChecksRunning), Role: Muted}}}
	case len(v.Checks) == 0 && pending:
		return []Cell{{{Text: "  no check has reported: a required one may be waiting", Role: Muted}}}
	case len(v.Checks) == 0:
		return []Cell{{{Text: "  GitHub listed no check that failed", Role: Muted}}}
	}
	shown := v.Checks[:min(len(v.Checks), detailLines)]
	var out []Cell
	for _, c := range shown {
		mark, role := "✗", Warn
		if c.Running {
			mark, role = "~", Prompt
		}
		out = append(out, Cell{{Text: "  " + mark + " ", Role: role}, {Text: c.Name, Role: Text}})
	}
	if more := max(v.ChecksFailed+v.ChecksRunning, len(v.Checks)) - len(shown); more > 0 {
		out = append(out, Cell{{Text: fmt.Sprintf("  … %d more", more), Role: Dim}})
	}
	return out
}

// Quiet is the part shown when no section has anything to show.
func Quiet(it Item) Part {
	if it.Kind == KindWorktree {
		if it.Tree.Prunable {
			return Part{Head: head("DIRECTORY GONE", Warn, ""), Lines: []Cell{{{Text: "  git still lists it; d prunes it", Role: Muted}}}}
		}
		return Part{Head: head("CLEAN", OK, ""), Lines: []Cell{{{Text: "  nothing uncommitted, nothing unpushed", Role: Muted}}}}
	}
	return Part{Head: head("NOTHING TO DO", OK, ""), Lines: []Cell{{{Text: "  up to date, nothing uncommitted, nothing unpushed", Role: Muted}}}}
}

func text(c Cell) string {
	var b strings.Builder
	for _, s := range c {
		b.WriteString(s.Text)
	}
	return b.String()
}
