package local

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/babarot/gh-tidy/internal/gittest"
	"github.com/babarot/gh-tidy/internal/model"
)

func subjects(cs []model.Commit) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Subject)
	}
	return out
}

func TestDetail(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// Two commits on origin's main the local main lacks.
	w.push(2)
	gittest.Git(t, w.clone, "fetch", "-q")
	// A branch pushed, then one commit more; a change, and a stash.
	gittest.Git(t, w.clone, "checkout", "-q", "-b", "fix")
	gittest.Commit(t, w.clone, "f", "fix one")
	gittest.Git(t, w.clone, "push", "-q", "-u", "origin", "fix")
	gittest.Commit(t, w.clone, "f", "fix two")
	write(t, filepath.Join(w.clone, "f"), "changed")
	write(t, filepath.Join(w.clone, "s"), "stashed")
	gittest.Git(t, w.clone, "stash", "push", "-q", "-u", "-m", "kept for later", "--", "s")
	write(t, filepath.Join(w.clone, "new.txt"), "new")
	// A worktree on a branch never pushed.
	wt := filepath.Join(w.base, "wt", "solo")
	gittest.Git(t, w.clone, "worktree", "add", "-q", "-b", "solo", wt, "main")
	gittest.Commit(t, wt, "solo", "solo work")

	c := Scan(ctx, w.clone)
	d := Detail(ctx, c, "")
	if d.Err != "" {
		t.Fatal(d.Err)
	}
	if got := subjects(d.Incoming); len(got) != 2 || got[0] != "pushed 2" {
		t.Errorf("incoming = %v", got)
	}
	if got := subjects(d.Unpushed); len(got) != 1 || got[0] != "fix two" || d.NeverPushed {
		t.Errorf("unpushed = %v, never pushed %v", got, d.NeverPushed)
	}
	if got := subjects(d.BehindMain); len(got) != 2 {
		t.Errorf("behind main = %v", got)
	}
	if len(d.Files) != 2 || d.Files[0].XY != " M" || d.Files[0].Path != "f" || d.Files[1].XY != "??" {
		t.Errorf("files = %+v", d.Files)
	}
	if len(d.Stash) != 1 || d.Stash[0].Ref != "stash@{0}" {
		t.Errorf("stash = %+v", d.Stash)
	}

	d = Detail(ctx, c, wt)
	if got := subjects(d.Unpushed); len(got) != 1 || got[0] != "solo work" || !d.NeverPushed {
		t.Errorf("worktree unpushed = %v, never pushed %v", got, d.NeverPushed)
	}
	if len(d.Incoming) != 0 || len(d.Stash) != 0 || len(d.Files) != 0 {
		t.Errorf("worktree detail = %+v", d)
	}

	f, err := WorktreeFacts(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !f.WorkedOn["solo"] || len(f.WorkedOn) != 1 || !f.Merged["main"] {
		t.Errorf("worktree facts = %+v", f)
	}
}
