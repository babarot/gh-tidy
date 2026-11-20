package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
)

const work = "/work"

func (w *world) engineAt(roots ...string) *Engine {
	e := New(Options{Roots: roots, Local: w.local, GitHub: w.gh, Store: w.store, Now: w.clock.now})
	if err := e.Start(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	return e
}

func clonesOf(o model.Owner) []string {
	var out []string
	for _, r := range o.Rows {
		if r.Clone != "" {
			out = append(out, string(r.Clone))
		}
	}
	return out
}

// An owner's clones are read from every root, into one list, and so are
// all's.
func TestEveryRootIsRead(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.addAt(work, "me", "api", "me/api", nil)
	w.local.addAt(work, "acme", "web", "acme/web", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "api")
	e := w.engineAt(root, work)
	e.Open("me")
	idle(t, e)
	if got := clonesOf(e.Owner()); len(got) != 2 {
		t.Errorf("me's clones = %q; want app from /src and api from /work", got)
	}
	e.Open(All)
	idle(t, e)
	if got := clonesOf(e.Owner()); len(got) != 3 {
		t.Errorf("all's clones = %q", got)
	}
}

// A root that cannot be read keeps what is known of its clones: what is
// missing is forgotten only once every root was read, with all of them.
func TestARootThatCannotBeReadForgetsNothing(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.addAt(work, "me", "api", "me/api", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "api")
	e := w.engineAt(root, work)
	e.Open("me")
	idle(t, e)
	w.local.mu.Lock()
	w.local.findErr = map[string]error{filepath.Join(work, "github.com", "me"): fs.ErrPermission}
	w.local.mu.Unlock()
	e.Refresh()
	idle(t, e)
	if r := rowOf(e.Owner(), "/work/github.com/me/api"); r == nil {
		t.Errorf("the clone of the root that could not be read was forgotten: %q", clonesOf(e.Owner()))
	}
	if r := rowOf(e.Owner(), "/src/github.com/me/app"); r == nil {
		t.Error("the other root's clone was forgotten")
	}
}

// A clone kept from a root no longer among the roots is not shown, nor
// acted on, before the read; a repository of the list stays as not
// cloned, though the list answers 304 and makes no row.
func TestACloneOfARootTakenOutStaysListed(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.addAt(work, "me", "api", "me/api", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "api")
	e := w.engineAt(root, work)
	e.Open("me")
	idle(t, e)

	release := make(chan struct{})
	w.gh.block = map[string]chan struct{}{"me": release}
	e2 := w.engineAt(root)
	e2.Open("me")
	if r := rowOf(e2.Owner(), "/work/github.com/me/api"); r != nil {
		t.Error("a clone of the root taken out came from the cache")
	}
	close(release)
	idle(t, e2)
	if r := rowOf(e2.Owner(), "github.com/me/api"); r == nil || r.Clone != "" || !r.Listed.Has {
		t.Errorf("api after a list answered 304 = %+v; want it as not cloned", r)
	}
}

// c clones into the root that already holds the owner's directory, else
// into the first root.
func TestCloneGoesWhereTheOwnerIs(t *testing.T) {
	w := newWorld(t)
	first, second := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(second, "github.com", "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := w.engineAt(first, second)
	if got := e.cloneDir("acme"); got != filepath.Join(second, "github.com", "acme") {
		t.Errorf("acme clones into %s", got)
	}
	if got := e.cloneDir("me"); got != filepath.Join(first, "github.com", "me") {
		t.Errorf("me clones into %s", got)
	}
}

// An owner's directory two roots reach through a symlink is read once,
// from the first root, so that no clone has two rows.
func TestAnOwnerDirectoryReachedTwiceIsReadOnce(t *testing.T) {
	w := newWorld(t)
	first, second := t.TempDir(), t.TempDir()
	me := filepath.Join(first, "github.com", "me")
	if err := os.MkdirAll(me, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(second, "github.com"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(me, filepath.Join(second, "github.com", "me")); err != nil {
		t.Fatal(err)
	}
	w.local.addAt(first, "me", "app", "me/app", nil)
	w.local.addAt(second, "me", "app", "me/app", nil) // the same clone, through the link
	w.gh.setList("me", github.Complete, "e1", "app")
	e := w.engineAt(first, second)
	e.Open("me")
	idle(t, e)
	if got := clonesOf(e.Owner()); len(got) != 1 || got[0] != filepath.Join(me, "app") {
		t.Errorf("clones = %q; want the one of the first root", got)
	}
}
