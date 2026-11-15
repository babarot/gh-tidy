// Package app puts gh-tidy together: it reads the flags and the settings,
// builds the engine from the concrete packages, decides which owner to
// open, and runs the screen.
package app

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/engine"
	"github.com/babarot/gh-tidy/internal/github"
	"github.com/babarot/gh-tidy/internal/local"
	"github.com/babarot/gh-tidy/internal/locate"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
	"github.com/babarot/gh-tidy/internal/store"
	"github.com/babarot/gh-tidy/internal/tui"
	"github.com/babarot/gh-tidy/internal/version"
)

// Name is the program's name: its command, and the directories its files
// go in.
const Name = "gh-tidy"

const usage = `gh tidy keeps one owner's local clones up to date and clear of what you are
done with, so they are ready to hand to a coding agent.

Usage:
  %s [flags] [OWNER | HOST/OWNER]

Without OWNER, it opens the owner of the clone it is started in, the owner
directory under the root it is started in, the owner opened last, or you.

Settings are in %s, and the log is written to %s.

Flags:
`

// Run runs gh-tidy with args (without the program's name); cmd is how
// the usage names it.
func Run(cmd string, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), usage, cmd, locate.Abbrev(config.Path()), locate.Abbrev(logging.DefaultPath(Name)))
		fs.PrintDefaults()
	}
	showVersion := fs.Bool("version", false, "print the version")
	debug := fs.Bool("debug", false, "log at debug level (also GH_TIDY_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintln(stderr, Name, version.Version)
		return nil
	}
	if fs.NArg() > 1 {
		return errors.New("one owner at most")
	}
	log, closeLog := openLog(*debug, stderr)
	defer closeLog()

	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := locate.Root(ctx, cfg.Core.Root)
	client, err := github.NewClient(github.Options{Logger: log})
	if err != nil {
		return err
	}
	st := store.Store{CacheDir: xdg("XDG_CACHE_HOME", ".cache"), StateDir: xdg("XDG_STATE_HOME", filepath.Join(".local", "state"))}
	eng := engine.New(engine.Options{Root: root, Local: disk{}, GitHub: client, Store: st, Logger: log})
	if err := eng.Start(ctx); err != nil {
		log.Warn("who is signed in is not known: every owner is read as another user's", logging.KeyReason, err.Error())
	}
	viewer := eng.Viewer()
	host := client.Host()
	state, err := st.LoadState()
	if err != nil {
		log.Warn("state not read", logging.KeyReason, err.Error())
	}
	account := store.AccountKey(host, viewer.Login)
	owner, sel := startOwner(ctx, fs.Arg(0), root, state.LastOwner[account], viewer.Login)
	log.Info("start", logging.KeyVersion, version.Version, logging.KeyOwner, host+"/"+owner)

	m, err := tui.New(tui.Options{
		Engine: eng, Config: cfg, Owners: owners(root, host, viewer, cfg.Owners), Owner: owner, Select: sel,
		Lens: state.Lens, PreviewHeight: state.PreviewHeight, Home: home(),
		Save: func(owner, lens string, ph int) {
			if state.LastOwner == nil {
				state.LastOwner = map[string]string{}
			}
			state.LastOwner[account], state.Lens, state.PreviewHeight = owner, lens, ph
			if err := st.SaveState(state); err != nil {
				log.Warn("state not saved", logging.KeyReason, err.Error())
			}
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", locate.Abbrev(config.Path()), err)
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// startOwner decides the owner to open: the one given, the one where gh
// tidy started (with the clone it started in selected), the one opened
// last, the viewer, or all.
func startOwner(ctx context.Context, arg, root, last, viewer string) (owner, sel string) {
	if arg != "" {
		if _, o, ok := strings.Cut(arg, "/"); ok {
			return o, ""
		}
		return arg, ""
	}
	if cwd, err := os.Getwd(); err == nil {
		if t, err := locate.Resolve(ctx, cwd, root); err == nil && t.Kind == locate.Owner {
			if t.Select != "" {
				sel = string(model.NewCloneKey(t.Select))
			}
			return t.Owner, sel
		}
	}
	return cmp.Or(last, viewer, tui.All), ""
}

// owners are the owners offered: the viewer, the viewer's organizations,
// the owners with clones under the root and those the settings add, less
// those they hide, by name.
func owners(root, host string, v github.Viewer, set config.Owners) []string {
	var out []string
	add := func(o string) {
		if o != "" && !slices.ContainsFunc(out, func(s string) bool { return strings.EqualFold(s, o) }) {
			out = append(out, o)
		}
	}
	add(v.Login)
	for _, o := range v.Orgs {
		add(o)
	}
	if dirs, err := local.Owners(filepath.Join(root, host)); err == nil {
		for _, o := range dirs {
			add(o)
		}
	}
	strip := func(o string) string {
		if h, rest, ok := strings.Cut(o, "/"); ok && strings.EqualFold(h, host) {
			return rest
		}
		return o
	}
	for _, o := range set.Add {
		add(strip(o))
	}
	out = slices.DeleteFunc(out, func(o string) bool {
		return slices.ContainsFunc(set.Hide, func(h string) bool { return strings.EqualFold(strip(h), o) })
	})
	slices.SortFunc(out, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}

// disk is the clones on disk, read with git, as the engine needs them.
type disk struct{}

func (disk) Owners(dir string) ([]string, error) { return local.Owners(dir) }
func (disk) Find(dir string) ([]string, error)   { return local.Find(dir) }
func (disk) Scan(ctx context.Context, path string) model.Clone {
	return local.Scan(ctx, path)
}
func (disk) SetDefault(ctx context.Context, c model.Clone, name string) model.Clone {
	local.SetDefault(ctx, &c, name)
	return c
}
func (disk) RemoteAhead(ctx context.Context, path, name, oid string) model.Ahead {
	return local.RemoteAhead(ctx, path, name, oid)
}
func (disk) WorktreeFacts(ctx context.Context, c model.Clone) (model.CleanupFacts, error) {
	return local.WorktreeFacts(ctx, c)
}
func (disk) Detail(ctx context.Context, c model.Clone, tree string) model.Detail {
	return local.Detail(ctx, c, tree)
}

// xdg is the program's directory under the XDG directory env names, or
// under fallback in the home directory.
func xdg(env, fallback string) string {
	dir := os.Getenv(env)
	if dir == "" {
		dir = filepath.Join(home(), fallback)
	}
	return filepath.Join(dir, Name)
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// openLog opens the log file, at debug level when asked by the flag or by
// GH_TIDY_LOG_LEVEL. A log that cannot be opened is said on stderr and
// left out, since gh tidy works without one.
func openLog(debug bool, stderr io.Writer) (*slog.Logger, func()) {
	level, err := logging.ParseLevel(os.Getenv("GH_TIDY_LOG_LEVEL"))
	if err != nil {
		fmt.Fprintln(stderr, "gh tidy: GH_TIDY_LOG_LEVEL:", err)
	}
	if debug {
		level = slog.LevelDebug
	}
	log, c, err := logging.Open(logging.DefaultPath(Name), level)
	if err != nil {
		fmt.Fprintln(stderr, "gh tidy: no log:", err)
		return logging.Discard(), func() {}
	}
	return log.With(logging.KeyRun, logging.NewID()), func() { _ = c.Close() }
}
