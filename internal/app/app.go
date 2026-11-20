// Package app puts gh-tidy together: it reads the flags and the settings,
// builds the engine from the concrete packages, decides which owner to
// open, and runs the screen.
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

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
directory under a root it is started in, the owner opened last, or you.

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
	printConfig := fs.Bool("print-config", false, "print every setting at its default, commented out, to start a config file from")
	asJSON := fs.Bool("json", false, "print the owner's clones as JSON, read from disk alone, instead of opening the list")
	debug := fs.Bool("debug", false, "log at debug level (also GH_TIDY_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *printConfig {
		fmt.Fprint(os.Stdout, config.Template)
		return nil
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
	// Before ctrl+c is caught, so that it ends the question about the
	// roots as it ends any program.
	wd, _ := os.Getwd()
	roots, err := roots(context.Background(), cfg.Core.Roots, config.Path(), wd, ask{in: os.Stdin, out: stderr, terminal: !*asJSON && isTerminal(os.Stdin) && isTerminal(os.Stderr)})
	if err != nil {
		return err
	}
	for _, r := range roots {
		if _, err := os.Stat(r); os.IsNotExist(err) {
			log.Warn("a root is not there: no clone is read from it", logging.KeyRoot, r)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *asJSON {
		host, owner := locate.DefaultHost, ""
		if h, o, ok := strings.Cut(fs.Arg(0), "/"); ok {
			host, owner = h, o
		} else {
			owner, _ = startOwner(ctx, fs.Arg(0), roots, "", "")
		}
		if owner == "" || owner == tui.All {
			return errors.New("--json needs an owner: pass one, or run it in an owner's directory")
		}
		var dirs []string
		for _, root := range roots {
			dirs = append(dirs, filepath.Join(root, host, owner))
		}
		return printJSON(ctx, os.Stdout, dirs)
	}
	client, err := github.NewClient(github.Options{Logger: log})
	if err != nil {
		return err
	}
	st := store.Store{CacheDir: xdg("XDG_CACHE_HOME", ".cache"), StateDir: xdg("XDG_STATE_HOME", filepath.Join(".local", "state"))}
	eng := engine.New(engine.Options{Roots: roots, Local: disk{}, Change: changer{remove: cfg.Cleanup.WorktreeRemove}, Cloner: cloner{},
		GitHub: client, Store: st, Logger: log})
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
	owner, sel := startOwner(ctx, fs.Arg(0), roots, state.LastOwner[account], viewer.Login)
	log.Info("start", logging.KeyVersion, version.Version, logging.KeyOwner, host+"/"+owner)

	m, err := tui.New(tui.Options{
		Engine: eng, Commands: programs{}, Cwd: cwd(), Config: cfg, Owners: owners(roots, host, viewer, cfg.Owners), Roots: roots, Owner: owner, Select: sel,
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

// printJSON writes the clones under dirs (the owner's directory in each
// root) as JSON, read from disk alone. A directory two roots reach
// through a symlink is read once.
func printJSON(ctx context.Context, w io.Writer, dirs []string) error {
	var paths []string
	var seen []os.FileInfo
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err == nil {
			if slices.ContainsFunc(seen, func(s os.FileInfo) bool { return os.SameFile(s, fi) }) {
				continue
			}
			seen = append(seen, fi)
		}
		ps, err := local.Find(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		paths = append(paths, ps...)
	}
	clones := make([]model.Clone, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, p := range paths {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			clones[i] = local.Scan(ctx, p)
		})
	}
	wg.Wait()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(clones)
}

// startOwner decides the owner to open: the one given, the one where gh
// tidy started (with the clone it started in selected), the one opened
// last, the viewer, or all.
func startOwner(ctx context.Context, arg string, roots []string, last, viewer string) (owner, sel string) {
	if arg != "" {
		if _, o, ok := strings.Cut(arg, "/"); ok {
			return o, ""
		}
		return arg, ""
	}
	if cwd, err := os.Getwd(); err == nil {
		if t, err := locate.Resolve(ctx, cwd, roots); err == nil && t.Kind == locate.Owner {
			if t.Select != "" {
				sel = string(model.NewCloneKey(t.Select))
			}
			return t.Owner, sel
		}
	}
	return cmp.Or(last, viewer, tui.All), ""
}

// owners are the owners offered: the viewer, the viewer's organizations,
// the owners with clones under a root and those the settings add, less
// those they hide, by name.
func owners(roots []string, host string, v github.Viewer, set config.Owners) []string {
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
	for _, root := range roots {
		if dirs, err := local.Owners(filepath.Join(root, host)); err == nil {
			for _, o := range dirs {
				add(o)
			}
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

// xdg is the program's directory under the XDG directory env names, or
// under fallback in the home directory.
func xdg(env, fallback string) string {
	dir := os.Getenv(env)
	if dir == "" {
		dir = filepath.Join(home(), fallback)
	}
	return filepath.Join(dir, Name)
}

// cwd is where gh tidy runs, with symlinks resolved as git resolves the
// paths of worktrees: d keeps the worktree holding it, and a path reached
// through a link would not be seen inside it.
func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return resolved(d)
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
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
