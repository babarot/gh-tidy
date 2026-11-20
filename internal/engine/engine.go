// Package engine holds what gh-tidy knows of owners and their clones, and
// is the only one that changes it. It decides what to read and when, runs
// it with a limit on each kind of outside access, keeps within GitHub's
// rate limits, and hands the screen copies of what it holds. It does not
// import Bubble Tea: it reports changes through a channel, and is tested
// against a made-up git and GitHub without a terminal.
package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/store"
)

// Local is what the engine needs of the clones on disk.
type Local interface {
	Owners(dir string) ([]string, error)
	Find(dir string) ([]string, error)
	Scan(ctx context.Context, path string) model.Clone
	// SetDefault measures c against name, GitHub's default branch.
	SetDefault(ctx context.Context, c model.Clone, name string) model.Clone
	RemoteAhead(ctx context.Context, path, name, remoteOid string) model.Ahead
	// WorktreeFacts reads what judging c's linked worktrees needs.
	WorktreeFacts(ctx context.Context, c model.Clone) (model.CleanupFacts, error)
	// Detail reads what the preview shows of c, or of its worktree at
	// path when worktree is not empty.
	Detail(ctx context.Context, c model.Clone, worktree string) model.Detail
}

// GitHub is what the engine needs of GitHub.
type GitHub interface {
	Host() string
	Viewer(ctx context.Context) (github.Viewer, github.Rate, error)
	List(ctx context.Context, owner string, kind github.OwnerKind, prev github.Pages, maxPages int) (github.Listing, error)
	Heavy(ctx context.Context, slugs []string, want github.Want) ([]github.HeavyRepo, github.Rate, error)
}

// Cloner clones a repository into a directory.
type Cloner interface {
	Clone(ctx context.Context, host, slug, dir string) error
}

// Store keeps owners between starts; nil keeps nothing (--no-cache).
type Store interface {
	LoadOwner(host, viewer, owner string) (store.OwnerCache, bool, error)
	SaveOwner(store.OwnerCache) error
}

// All is the owner that stands for every owner under the roots.
const All = "all"

// Limits on each kind of outside access at once, and the sizes of what is
// read. Constants: they guard the load put on the machine and on GitHub,
// which no user needs to tune.
const (
	networkAtOnce = 4  // git talking to GitHub: fetch
	apiAtOnce     = 2  // requests to GitHub's API; GitHub asks for them one after another
	heavyBatch    = 10 // repositories a GraphQL query asks for by name
	maxListPages  = 20 // pages of an owner's list read unasked
	// askedListPages bounds a list the user asked for: far past any
	// owner's, only against a list that never ends.
	askedListPages = 1000
	keepOwners     = 4 // owners kept in memory besides the one shown
)

// How long values stay fresh before they are read again unasked.
const (
	listFresh    = 5 * time.Minute
	countsFresh  = 10 * time.Minute
	unlistedOids = 10 * time.Minute // oids of clones no list covers, read again by time; also what GitHub would not give
	retryFailed  = time.Minute      // a read that went wrong, tried again
)

// Options build an Engine.
type Options struct {
	// Roots are where clones live, as <root>/<host>/<owner>/<repo>, in
	// order, resolved and each once (locate.Roots).
	Roots  []string
	Local  Local
	Change Changer
	GitHub GitHub
	Store  Store
	// Cloner clones a repository; nil refuses to.
	Cloner Cloner
	Logger *slog.Logger
	Now    func() time.Time
}

// Engine is what gh-tidy knows, and the work of reading and changing it.
type Engine struct {
	roots  []string
	host   string
	local  Local
	change Changer
	gh     GitHub
	store  Store
	cloner Cloner
	log    *slog.Logger
	now    func() time.Time

	localPool, netPool, apiPool *pool
	idle                        *idleGroup
	budget                      *budget
	track                       *tracker

	mu      sync.Mutex
	ctx     context.Context
	viewer  github.Viewer
	known   bool // the viewer was read
	owners  map[string]*owner
	shown   string // the owner on the screen
	used    []string
	needs   model.Need
	visible map[string]bool
	changed chan struct{}
	// lanes holds what is being done to each clone; epoch moves on when a
	// change starts and ends, so that a read begun before is dropped.
	lanes map[model.CloneKey]*hold
	epoch map[model.CloneKey]int64
	// details are what the preview shows of the rows asked for, and
	// detailing those being read.
	details   map[detailKey]model.Detail
	detailing map[detailKey]bool
	// reads counts the reads of each clone put, so that a detail read
	// from an older copy of the clone is dropped.
	reads map[model.CloneKey]int64
}

// detailKey is a clone, or one of its linked worktrees.
type detailKey struct {
	clone model.CloneKey
	tree  string
}

// owner is what the engine holds of one owner.
type owner struct {
	login       string
	kind        github.OwnerKind
	gen         int // moves on each time the owner is opened; older work is dropped
	cancel      context.CancelFunc
	ctx         context.Context
	rows        map[string]*model.Row
	list        model.Completeness
	listWhy     string
	listAt      time.Time
	pages       github.Pages
	fromCache   bool
	version     int
	pending     map[string]int           // work of each kind in flight, for the header
	writes      int                      // changes in flight: an owner with one is not let go
	inflight    map[model.RemoteKey]mark // repositories being read by name
	listing     int                      // the generation whose list is being read; 0 when none
	asked       bool                     // the user asked for the list: read it whole, whoever the owner is
	refresh     bool                     // r asked to read everything again: the next heavy read takes every row
	refreshList bool                     // and the next list read is asked for, as far as the owner's list is read at all
}

// mark is a repository being read by name: the fields asked for, and the
// generation that asked, so that a canceled read of an older one does not
// clear the mark of a newer one.
type mark struct {
	fields model.Need
	gen    int
}

// New returns an engine. Call Start before anything else.
func New(o Options) *Engine {
	idle := newIdleGroup()
	now := o.Now
	if now == nil {
		now = time.Now
	}
	log := o.Logger
	if log == nil {
		log = logging.Discard()
	}
	e := &Engine{roots: o.Roots, host: o.GitHub.Host(), local: o.Local, change: o.Change, gh: o.GitHub, store: o.Store, cloner: o.Cloner, log: log, now: now,
		lanes: map[model.CloneKey]*hold{}, epoch: map[model.CloneKey]int64{},
		details: map[detailKey]model.Detail{}, detailing: map[detailKey]bool{}, reads: map[model.CloneKey]int64{},
		localPool: newPool(runtime.NumCPU(), idle), netPool: newPool(networkAtOnce, idle), apiPool: newPool(apiAtOnce, idle),
		idle: idle, owners: map[string]*owner{}, visible: map[string]bool{}, changed: make(chan struct{}, 1)}
	e.budget = newBudget(now, log)
	e.track = newTracker(now)
	idle.onIdle = e.track.idled
	return e
}

// Start reads who is signed in, which decides whose lists may be read. A
// failure leaves every owner read as another user's (clones only), and is
// returned for the screen to say.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	e.ctx = ctx
	e.mu.Unlock()
	if e.change == nil {
		e.change = noChanger{}
	}
	v, rate, err := e.gh.Viewer(ctx)
	e.budget.saw(rate, err == nil)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		return err
	}
	e.viewer, e.known = v, true
	return nil
}

// Viewer is who is signed in; empty when it could not be read.
func (e *Engine) Viewer() github.Viewer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.viewer
}

// Changes tells that something changed; read Owner for what. Changes that
// come together are told once.
func (e *Engine) Changes() <-chan struct{} { return e.changed }

func (e *Engine) notify() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

// Idle returns when no work is queued or running, or ctx ends. For tests,
// and for quitting after the changes in flight.
func (e *Engine) Idle(ctx context.Context) error { return e.idle.wait(ctx) }

// SetNeeds tells what the screen shows: the union of what its columns and
// preview sections need, and the rows on the screen, read first. It reads
// what that adds.
func (e *Engine) SetNeeds(needs model.Need, visible []string) {
	e.mu.Lock()
	added := needs &^ e.needs
	e.needs = needs
	e.visible = map[string]bool{}
	for _, k := range visible {
		e.visible[k] = true
	}
	o := e.owners[e.shown]
	e.mu.Unlock()
	if added != 0 && o != nil {
		e.readHeavy(o)
	}
}

// Open shows owner (a login, or All): what is kept of it shows at once,
// and it is read again. Reads of the owner shown before are dropped;
// changes in flight go on.
func (e *Engine) Open(login string) {
	if strings.EqualFold(login, All) {
		login = All
	}
	e.mu.Lock()
	if prev := e.owners[e.shown]; prev != nil && prev.cancel != nil {
		prev.cancel() // the owner left, or opened again: a new reading starts
	}
	key := strings.ToLower(login)
	o := e.owners[key]
	if o == nil {
		o = &owner{login: login, rows: map[string]*model.Row{}, pending: map[string]int{}, inflight: map[model.RemoteKey]mark{}}
		e.owners[key] = o
		e.loadCache(o)
	}
	o.kind = e.kindOf(login)
	o.gen++
	o.refresh, o.refreshList = false, false // an r for an owner left was for then
	o.inflight = map[model.RemoteKey]mark{}
	o.listing = 0
	o.ctx, o.cancel = context.WithCancel(e.ctx)
	e.shown = key
	e.touch(key)
	e.mu.Unlock()
	defer e.busy()()
	e.track.load(login)
	e.localPool.drop()
	e.apiPool.drop()
	e.notify()
	if key == All {
		e.readAll(o)
		return
	}
	e.readOwner(o)
}

// kindOf tells who login is to the viewer. Without the viewer, every owner
// reads as another user's: only clones are looked up.
func (e *Engine) kindOf(login string) github.OwnerKind {
	if !e.known {
		return github.OwnerUser
	}
	return e.viewer.KindOf(login)
}

// touch marks key as used now, and lets go of the owners used longest ago
// beyond keepOwners, unless they have changes in flight.
func (e *Engine) touch(key string) {
	e.used = append(slices.DeleteFunc(e.used, func(k string) bool { return k == key }), key)
	for len(e.used) > keepOwners+1 {
		old := e.used[0]
		if o := e.owners[old]; o != nil && o.writes > 0 {
			break
		}
		delete(e.owners, old)
		e.used = e.used[1:]
	}
}

// alive is the check a task makes when its turn comes: its owner is still
// shown and has not been opened again since.
func (e *Engine) alive(o *owner, gen int) func() bool {
	return func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return o.gen == gen && e.owners[e.shown] == o
	}
}

// Owner returns a copy of what is held of the owner shown.
func (e *Engine) Owner() model.Owner {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.owners[e.shown]
	if o == nil {
		return model.Owner{Host: e.host}
	}
	out := model.Owner{Host: e.host, Login: o.login, All: e.shown == All, List: o.list, ListWhy: o.listWhy, ListAt: o.listAt,
		FromCache: o.fromCache, Version: o.version, Reading: reading(o)}
	keys := make([]string, 0, len(o.rows))
	for k := range o.rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out.Rows = append(out.Rows, *o.rows[k])
	}
	return out
}

func reading(o *owner) string {
	for _, k := range []string{"clones", "list", "GitHub"} {
		if o.pending[k] > 0 {
			return "reading " + k
		}
	}
	return ""
}

// changedOwner records a change to o, and tells the screen.
func (e *Engine) changedOwner(o *owner) {
	o.version++
	e.notify()
}

// begin and end count work of a kind in flight for o, for the header; end
// saves o once nothing is left.
func (e *Engine) begin(o *owner, kind string) {
	e.mu.Lock()
	o.pending[kind]++
	e.mu.Unlock()
}

func (e *Engine) end(o *owner, kind string) {
	e.mu.Lock()
	o.pending[kind]--
	done := true
	for _, n := range o.pending {
		if n > 0 {
			done = false
		}
	}
	e.changedOwner(o)
	var c store.OwnerCache
	save := done && e.store != nil && o.login != All && e.known
	if save {
		c = e.cacheOf(o)
	}
	e.mu.Unlock()
	if save {
		if err := e.store.SaveOwner(c); err != nil {
			e.log.Warn("could not keep the owner", logging.KeyOwner, o.login, "err", err.Error())
		}
	}
}

func (e *Engine) cacheOf(o *owner) store.OwnerCache {
	c := store.OwnerCache{Host: e.host, Viewer: e.viewer.Login, Owner: o.login, List: o.list, ListWhy: o.listWhy, ListAt: o.listAt,
		ETags: o.pages.ETags, Ended: o.pages.Ended, SavedAt: e.now()}
	for _, r := range o.rows {
		c.Rows = append(c.Rows, *r)
	}
	return c
}

func (e *Engine) loadCache(o *owner) {
	if e.store == nil || o.login == All || !e.known {
		return
	}
	c, ok, err := e.store.LoadOwner(e.host, e.viewer.Login, o.login)
	if err != nil {
		e.log.Warn("could not read what was kept", logging.KeyOwner, o.login, "err", err.Error())
	}
	if !ok {
		return
	}
	o.list, o.listWhy, o.listAt = c.List, c.ListWhy, c.ListAt
	o.pages = github.Pages{ETags: c.ETags, Ended: c.Ended, Completeness: toGitHub(c.List), Reason: c.ListWhy}
	for i := range c.Rows {
		r := c.Rows[i]
		o.rows[r.Key()] = &r
	}
	// A clone kept from a root no longer among the roots is not this
	// owner's now: nothing shows it or acts on it before the read.
	e.dropClones(o, func(r *model.Row) bool { return !e.inOwnerDir(string(r.Clone), o.login) })
	o.fromCache = true
}

// ownerDirs are where the clones of login live, a directory under each
// root, in the roots' order. A directory two roots reach (through a
// symlink, or two spellings on a disk that ignores case) is read once,
// from the first root: compared only to tell, so that a clone's path
// stays under its root.
func (e *Engine) ownerDirs(login string) []string {
	var out []string
	var seen []os.FileInfo
	for _, root := range e.roots {
		d := filepath.Join(root, e.host, login)
		if fi, err := os.Stat(d); err == nil {
			if slices.ContainsFunc(seen, func(s os.FileInfo) bool { return os.SameFile(s, fi) }) {
				continue
			}
			seen = append(seen, fi)
		}
		out = append(out, d)
	}
	return out
}

// hostDirs are the roots' directories of the host, where the owners are.
func (e *Engine) hostDirs() []string {
	out := make([]string, len(e.roots))
	for i, root := range e.roots {
		out[i] = filepath.Join(root, e.host)
	}
	return out
}

func toGitHub(c model.Completeness) github.Completeness {
	switch c {
	case model.ListComplete:
		return github.Complete
	case model.ListPartial:
		return github.Partial
	case model.ListUnreadable:
		return github.Unreadable
	}
	return github.Unknown
}

func fromGitHub(c github.Completeness) model.Completeness {
	switch c {
	case github.Complete:
		return model.ListComplete
	case github.Partial:
		return model.ListPartial
	case github.Unreadable:
		return model.ListUnreadable
	}
	return model.ListUnknown
}
