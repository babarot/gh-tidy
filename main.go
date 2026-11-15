// Command gh-tidy keeps one GitHub owner's local clones up to date and clear
// of finished work, so they are ready to hand to a coding agent. It lists the
// clones with what stands in the way: how far each is from its upstream and
// its default branch, what is not committed or not pushed, and its worktrees.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/locate"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/repo"
	"github.com/babarot/gh-tidy/internal/tui"
	"github.com/babarot/gh-tidy/internal/version"
)

const usage = `gh tidy keeps one owner's local clones up to date and clear of what you are
done with, so they are ready to hand to a coding agent. It lists the clones
with what stands in the way: how far each is from its upstream and its default
branch, what is not committed or not pushed, and its worktrees.

Usage:
  gh tidy [flags] [OWNER | HOST/OWNER]

Without OWNER, the owner comes from where it is started: the owner of the
clone or worktree it is in, the owner directory under the root
(<root>/<host>/<owner>), or the owner picker at the root.

Settings are in %s, and the log is written to %s.

Flags:
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gh tidy:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("gh-tidy", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), usage, locate.Abbrev(config.Path()), locate.Abbrev(logging.DefaultPath(name)))
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "print the clones as JSON instead of opening the list")
	showVersion := fs.Bool("version", false, "print the version")
	debug := fs.Bool("debug", false, "log at debug level (also GH_TIDY_LOG_LEVEL)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("gh-tidy", version.Version)
		return nil
	}
	if fs.NArg() > 1 {
		return errors.New("one owner at most")
	}

	log, closeLog := openLog(*debug)
	defer closeLog()

	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := locate.Root(ctx, cfg.Core.Root)
	target, err := resolve(ctx, root, cfg, fs.Arg(0))
	if err != nil {
		return err
	}
	log.Info("start", logging.KeyVersion, version.Version, logging.KeyOwner, target.Host+"/"+target.Owner, "json", *asJSON)
	if *asJSON {
		if target.Kind != locate.Owner {
			return errors.New("--json needs an owner: pass one, or run it in an owner's directory")
		}
		return printJSON(ctx, target.Dir(root))
	}
	m, err := tui.New(ctx, cfg, root, target)
	if err != nil {
		return fmt.Errorf("%s: %w", locate.Abbrev(config.Path()), err)
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// name is the program's name: its command, and the directories its
// files go in.
const name = "gh-tidy"

// openLog opens the log file, at debug level when asked by the flag or by
// GH_TIDY_LOG_LEVEL. A log that cannot be opened is said on stderr and
// left out, since gh tidy works without one.
func openLog(debug bool) (*slog.Logger, func()) {
	level, err := logging.ParseLevel(os.Getenv("GH_TIDY_LOG_LEVEL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh tidy: GH_TIDY_LOG_LEVEL:", err)
	}
	if debug {
		level = slog.LevelDebug
	}
	log, c, err := logging.Open(logging.DefaultPath(name), level)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gh tidy: no log:", err)
		return logging.Discard(), func() {}
	}
	return log.With(logging.KeyRun, logging.NewID()), func() { _ = c.Close() }
}

// resolve picks what to open: the owner given, else what cwd says, else the
// configured default owner, else the picker.
func resolve(ctx context.Context, root string, cfg config.File, arg string) (locate.Target, error) {
	if arg != "" {
		host, owner := locate.DefaultHost, arg
		if h, o, ok := strings.Cut(arg, "/"); ok {
			host, owner = h, o
		}
		return locate.Target{Kind: locate.Owner, Host: host, Owner: owner}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return locate.Target{}, err
	}
	t, err := locate.Resolve(ctx, cwd, root)
	if errors.Is(err, locate.ErrOutside) {
		if cfg.Core.DefaultOwner != "" {
			return locate.Target{Kind: locate.Owner, Host: locate.DefaultHost, Owner: cfg.Core.DefaultOwner}, nil
		}
		return locate.Target{Kind: locate.Picker, Host: locate.DefaultHost}, nil
	}
	return t, err
}

func printJSON(ctx context.Context, dir string) error {
	paths, err := repo.Find(dir)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	var rs []*repo.Repo
	repo.ScanAll(ctx, paths, runtime.GOMAXPROCS(0)*2, func(r *repo.Repo) {
		mu.Lock()
		rs = append(rs, r)
		mu.Unlock()
	})
	repo.SortByName(rs)
	type out struct {
		*repo.Repo
		Err          string `json:",omitempty"`
		Score        int
		Unpushed     int
		NoUpstream   int
		GoneUpstream int
	}
	list := make([]out, 0, len(rs))
	for _, r := range rs {
		o := out{Repo: r, Score: r.Score(), Unpushed: len(r.Unpushed()), NoUpstream: len(r.NoUpstream()), GoneUpstream: len(r.GoneUpstream())}
		if r.Err != nil {
			o.Err = r.Err.Error()
		}
		r.Path = filepath.Clean(r.Path)
		list = append(list, o)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(list)
}
