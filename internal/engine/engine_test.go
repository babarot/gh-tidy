package engine

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/store"
)

const root = "/src"

// fakeLocal is clones on a made-up disk.
type fakeLocal struct {
	mu     sync.Mutex
	clones map[string]model.Clone // by path
	scans  int
}

func (f *fakeLocal) add(owner, name, slug string, edit func(*model.Clone)) {
	path := filepath.Join(root, "github.com", owner, name)
	c := model.Clone{Key: model.NewCloneKey(path), Path: path, Name: name, Default: "main", DefaultSource: model.DefaultFromOriginHEAD, DefaultOid: "local-" + name}
	if slug != "" {
		c.OriginURL, c.Host, c.Slug, c.Remote = "git@github.com:"+slug+".git", "github.com", slug, model.NewRemoteKey("github.com", slug)
	}
	if edit != nil {
		edit(&c)
	}
	f.mu.Lock()
	f.clones[path] = c
	f.mu.Unlock()
}

func (f *fakeLocal) Owners(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for p := range f.clones {
		if o := filepath.Base(filepath.Dir(p)); filepath.Dir(filepath.Dir(p)) == filepath.Clean(dir) && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeLocal) Find(dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for p := range f.clones {
		if filepath.Dir(p) == dir {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeLocal) Scan(_ context.Context, path string) model.Clone {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	return f.clones[path]
}

func (f *fakeLocal) RemoteAhead(_ context.Context, path, _, oid string) model.Ahead {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.clones[path]
	return model.Ahead{Remote: oid, Local: c.DefaultOid, Ahead: oid != c.DefaultOid}
}

// fakeGitHub is a made-up GitHub at the level the engine asks it.
type fakeGitHub struct {
	mu       sync.Mutex
	viewer   github.Viewer
	lists    map[string]github.Listing // by lower-case owner
	heavy    map[string]github.HeavyRepo
	renamed  map[string]string
	rate     github.Rate
	listed   []string // owners whose list was asked for
	asked    []string // slugs asked for by name
	wants    []github.Want
	timeouts int // batches larger than this many answer ErrTimeout
	limitErr error
	block    chan struct{} // when set, List waits for it
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{viewer: github.Viewer{Login: "me", Orgs: []string{"acme"}}, lists: map[string]github.Listing{},
		heavy: map[string]github.HeavyRepo{}, renamed: map[string]string{},
		rate: github.Rate{Limit: 5000, Remaining: 4000, Reset: time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC)}}
}

func (f *fakeGitHub) Host() string { return "github.com" }

func (f *fakeGitHub) Viewer(context.Context) (github.Viewer, github.Rate, error) {
	return f.viewer, f.rate, nil
}

func (f *fakeGitHub) List(ctx context.Context, owner string, _ github.OwnerKind, prev github.Pages, _ int) (github.Listing, error) {
	f.mu.Lock()
	block := f.block
	f.listed = append(f.listed, owner)
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return github.Listing{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.lists[strings.ToLower(owner)]
	if !ok {
		return github.Listing{}, github.ErrNoOwner
	}
	if len(prev.ETags) > 0 && slices.Equal(prev.ETags, l.Pages.ETags) {
		return github.Listing{Unchanged: true, Pages: l.Pages, Completeness: l.Completeness, Reason: l.Reason, Rate: f.rate}, nil
	}
	l.Rate = f.rate
	l.Rate.Cost = 1
	return l, nil
}

func (f *fakeGitHub) Heavy(_ context.Context, slugs []string, want github.Want) ([]github.HeavyRepo, github.Rate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.limitErr != nil {
		return nil, f.rate, f.limitErr
	}
	if f.timeouts > 0 && len(slugs) > f.timeouts {
		return nil, f.rate, github.ErrTimeout
	}
	f.asked = append(f.asked, slugs...)
	f.wants = append(f.wants, want)
	var out []github.HeavyRepo
	for _, s := range slugs {
		asked := model.NewRemoteKey("github.com", s)
		name := strings.ToLower(s)
		if to, ok := f.renamed[name]; ok {
			name = to
		}
		h, ok := f.heavy[name]
		if !ok {
			out = append(out, github.HeavyRepo{Asked: asked})
			continue
		}
		h.Asked, h.Key, h.Found = asked, model.NewRemoteKey("github.com", name), true
		out = append(out, h)
	}
	r := f.rate
	r.Cost = 1
	return out, r, nil
}

func (f *fakeGitHub) setList(owner string, complete github.Completeness, etag string, repos ...string) {
	l := github.Listing{Completeness: complete, Pages: github.Pages{ETags: []string{etag}, Ended: true, Completeness: complete}}
	for _, r := range repos {
		l.Repos = append(l.Repos, github.ListedRepo{Key: model.NewRemoteKey("github.com", owner+"/"+r), Name: r, NameWithOwner: owner + "/" + r, DefaultBranch: "main"})
	}
	f.lists[strings.ToLower(owner)] = l
}

type memStore struct {
	mu     sync.Mutex
	owners map[string]store.OwnerCache
}

func (m *memStore) LoadOwner(host, viewer, owner string) (store.OwnerCache, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.owners[owner]
	return c, ok, nil
}

func (m *memStore) SaveOwner(c store.OwnerCache) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.owners[c.Owner] = c
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type world struct {
	t     *testing.T
	local *fakeLocal
	gh    *fakeGitHub
	store *memStore
	clock *clock
}

func newWorld(t *testing.T) *world {
	return &world{t: t, local: &fakeLocal{clones: map[string]model.Clone{}}, gh: newFakeGitHub(), store: &memStore{owners: map[string]store.OwnerCache{}},
		clock: &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}}
}

func (w *world) engine() *Engine {
	e := New(Options{Root: root, Local: w.local, GitHub: w.gh, Store: w.store, Now: w.clock.now})
	if err := e.Start(context.Background()); err != nil {
		w.t.Fatal(err)
	}
	return e
}

func idle(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Idle(ctx); err != nil {
		t.Fatal("the engine did not settle:", err)
	}
}

func rowOf(o model.Owner, key string) *model.Row {
	for i := range o.Rows {
		if o.Rows[i].Key() == key {
			return &o.Rows[i]
		}
	}
	return nil
}

func TestOpenJoinsClonesAndTheList(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.add("me", "scratch", "", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "remote-app"}
	w.gh.heavy["me/tool"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	e := w.engine()
	e.Open("me")
	idle(t, e)

	o := e.Owner()
	if o.List != model.ListComplete || len(o.Rows) != 3 {
		t.Fatalf("list %v, %d rows", o.List, len(o.Rows))
	}
	app := rowOf(o, "/src/github.com/me/app")
	if app == nil || !app.Local.Has || !app.Listed.Has || app.Remote != "github.com/me/app" {
		t.Fatalf("app row = %+v", app)
	}
	if !app.Heavy.Has || app.Heavy.Value.DefaultOid != "remote-app" || !app.Ahead.Has || !app.Ahead.Value.Ahead {
		t.Errorf("app's default branch and GitHub being ahead: heavy %+v, ahead %+v", app.Heavy, app.Ahead)
	}
	tool := rowOf(o, "github.com/me/tool")
	if tool == nil || tool.Local.Has || !tool.Listed.Has || tool.Heavy.Has {
		t.Errorf("tool, not cloned, is read by name without github columns: %+v", tool)
	}
	if slices.Contains(w.gh.asked, "me/tool") || slices.Contains(w.gh.asked, "") {
		t.Errorf("asked for %v", w.gh.asked)
	}
	if len(w.store.owners["me"].Rows) != 3 {
		t.Errorf("the owner was not kept: %d rows", len(w.store.owners["me"].Rows))
	}
}

func TestNeedsAddWhatTheColumnsShow(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool", "fork")
	w.gh.lists["me"].Repos[2].Fork = true // app, tool, fork
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "local-app", Alerts: 2}
	w.gh.heavy["me/tool"] = github.HeavyRepo{Default: "main", DefaultOid: "t", Alerts: 1}
	e := w.engine()
	e.Open("me")
	idle(t, e)
	w.gh.asked = nil
	e.SetNeeds(model.NeedAlerts|model.NeedNotCloned, nil)
	idle(t, e)
	if !slices.Contains(w.gh.asked, "me/app") || !slices.Contains(w.gh.asked, "me/tool") || slices.Contains(w.gh.asked, "me/fork") {
		t.Errorf("asked for %v, want app and tool (not the fork)", w.gh.asked)
	}
	if want := w.gh.wants[len(w.gh.wants)-1]; !want.Alerts || want.Counts || want.CI {
		t.Errorf("want = %+v", want)
	}
	if tool := rowOf(e.Owner(), "github.com/me/tool"); !tool.Heavy.Has || tool.Heavy.Value.Alerts != 1 {
		t.Errorf("tool = %+v", tool.Heavy)
	}
}

func TestTheCacheShowsFirst(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool")
	e := w.engine()
	e.Open("me")
	idle(t, e)

	// The next start: the list blocks, yet the rows are there at once.
	w.gh.block = make(chan struct{})
	e2 := w.engine()
	e2.Open("me")
	o := e2.Owner()
	if !o.FromCache || len(o.Rows) != 2 || o.List != model.ListComplete {
		t.Fatalf("before reading: from cache %v, %d rows, %v", o.FromCache, len(o.Rows), o.List)
	}
	close(w.gh.block)
	idle(t, e2)
	if o := e2.Owner(); o.FromCache {
		t.Error("still marked as from the cache after reading again")
	}
}

func TestOpeningAnotherOwnerDropsTheOldReads(t *testing.T) {
	w := newWorld(t)
	w.local.add("acme", "api", "acme/api", nil)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("acme", github.Complete, "a1", "api")
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["acme/api"] = github.HeavyRepo{Default: "main", DefaultOid: "a"}
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "b"}
	w.gh.block = make(chan struct{})
	e := w.engine()
	e.Open("acme") // its list waits
	e.Open("me")   // and is canceled
	w.gh.mu.Lock()
	w.gh.block = nil
	w.gh.mu.Unlock()
	idle(t, e)
	if slices.Contains(w.gh.asked, "acme/api") {
		t.Errorf("the owner left was still read: %v", w.gh.asked)
	}
	if o := e.Owner(); o.Login != "me" || rowOf(o, "/src/github.com/me/app") == nil {
		t.Errorf("shown = %s, %d rows", o.Login, len(o.Rows))
	}
}

func TestAnOwnerNotYoursIsNotListed(t *testing.T) {
	w := newWorld(t)
	w.local.add("bigorg", "tool", "bigorg/tool", nil)
	w.gh.heavy["bigorg/tool"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	e := w.engine()
	e.Open("bigorg")
	idle(t, e)
	if slices.Contains(w.gh.listed, "bigorg") {
		t.Error("the list of an owner not yours was read unasked")
	}
	o := e.Owner()
	if o.List != model.ListPartial || !strings.Contains(o.ListWhy, "not your owner") {
		t.Errorf("list %v (%q)", o.List, o.ListWhy)
	}
	if r := rowOf(o, "/src/github.com/bigorg/tool"); !r.Heavy.Has {
		t.Error("the clone was not looked up by name")
	}
}

func TestRateLimitHoldsReadsBack(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	w.gh.rate.Remaining = 1000 // 20% left: under the floor for heavy reads
	e := w.engine()
	e.Open("me")
	idle(t, e)
	if len(w.gh.asked) != 0 {
		t.Errorf("heavy reads went at 20%% left: %v", w.gh.asked)
	}
	if len(w.gh.listed) == 0 {
		t.Error("the list, which costs nothing when unchanged, was held back too")
	}

	w.gh.rate.Remaining = 4000
	w.gh.limitErr = &github.RateLimitError{RetryAfter: 30 * time.Second}
	e2 := w.engine()
	e2.Open("me")
	idle(t, e2)
	if p := e2.budget.Paused(); p.IsZero() {
		t.Fatal("a secondary limit did not pause the reads")
	}
	w.gh.limitErr = nil
	e2.SetNeeds(model.NeedCounts, nil) // asks for more while paused
	idle(t, e2)
	if len(w.gh.asked) != 0 {
		t.Errorf("read while paused: %v", w.gh.asked)
	}
	w.clock.add(time.Minute)
	e2.SetNeeds(model.NeedCounts|model.NeedCI, nil)
	idle(t, e2)
	if !slices.Contains(w.gh.asked, "me/app") {
		t.Error("reads did not resume after the pause")
	}
}

func TestATimeoutSplitsTheBatch(t *testing.T) {
	w := newWorld(t)
	var names []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		w.local.add("me", n, "me/"+n, nil)
		w.gh.heavy["me/"+n] = github.HeavyRepo{Default: "main", DefaultOid: n}
		names = append(names, n)
	}
	w.gh.setList("me", github.Complete, "e1", names...)
	w.gh.timeouts = 3 // a query of more than three is given up on
	e := w.engine()
	e.Open("me")
	idle(t, e)
	for _, r := range e.Owner().Rows {
		if !r.Heavy.Has {
			t.Errorf("%s not read after the split", r.Key())
		}
	}
	if e.budget.batchSize(heavyBatch) >= heavyBatch {
		t.Error("the batch did not shrink after a timeout")
	}
}

func TestARenamedRepositoryJoinsItsClone(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "old-name", "me/old-name", nil)
	w.gh.setList("me", github.Complete, "e1", "new-name")
	w.gh.renamed["me/old-name"] = "me/new-name"
	w.gh.heavy["me/new-name"] = github.HeavyRepo{Default: "main", DefaultOid: "n"}
	e := w.engine()
	e.Open("me")
	idle(t, e)
	o := e.Owner()
	if len(o.Rows) != 1 {
		t.Fatalf("%d rows: the renamed repository is not joined with its clone", len(o.Rows))
	}
	if r := o.Rows[0]; r.Remote != "github.com/me/new-name" || !r.Listed.Has || !r.Heavy.Has {
		t.Errorf("row = %+v", r)
	}
}

func TestACompleteListDropsWhatIsGone(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.gh.setList("me", github.Complete, "e1", "app", "tool")
	e := w.engine()
	e.Open("me")
	idle(t, e)
	w.gh.setList("me", github.Complete, "e2") // both deleted on GitHub
	w.clock.add(listFresh + time.Second)
	e.Open("me")
	idle(t, e)
	o := e.Owner()
	if rowOf(o, "github.com/me/tool") != nil {
		t.Error("a repository gone from a complete list stays")
	}
	if r := rowOf(o, "/src/github.com/me/app"); r == nil || r.Listed.Has {
		t.Errorf("the clone of a repository gone from the list: %+v", r)
	}
}

func TestAllShowsEveryCloneAndAsksGitHubNothing(t *testing.T) {
	w := newWorld(t)
	w.local.add("me", "app", "me/app", nil)
	w.local.add("acme", "api", "acme/api", nil)
	w.gh.setList("me", github.Complete, "e1", "app")
	w.gh.heavy["me/app"] = github.HeavyRepo{Default: "main", DefaultOid: "x"}
	e := w.engine()
	e.Open("me")
	idle(t, e)
	listed, asked := len(w.gh.listed), len(w.gh.asked)
	e.Open(All)
	idle(t, e)
	o := e.Owner()
	if !o.All || len(o.Rows) != 2 {
		t.Fatalf("all: %v, %d rows", o.All, len(o.Rows))
	}
	if len(w.gh.listed) != listed || len(w.gh.asked) != asked {
		t.Error("all asked GitHub")
	}
	if r := rowOf(o, "/src/github.com/me/app"); r == nil || !r.Heavy.Has {
		t.Error("all did not take the GitHub values me last read")
	}
}
