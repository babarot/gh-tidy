// Command gh-fleet lists one GitHub owner's local clones with what each has
// to say: how far it is from its upstream and its default branch, what is not
// committed or not pushed, and its worktrees.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-fleet/internal/config"
	"github.com/babarot/gh-fleet/internal/locate"
	"github.com/babarot/gh-fleet/internal/repo"
	"github.com/babarot/gh-fleet/internal/tui"
	"github.com/babarot/gh-fleet/internal/version"
)

const usage = `gh fleet lists one owner's local clones: how far each is from its upstream
and its default branch, what is not committed or not pushed, and its worktrees.

Usage:
  gh fleet [flags] [OWNER | HOST/OWNER]

Without OWNER, the owner comes from where it is started: the owner of the
clone or worktree it is in, the owner directory under the root
(<root>/<host>/<owner>), or the owner picker at the root.

Settings are in %s.

Flags:
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gh fleet:", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("gh-fleet", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), usage, locate.Abbrev(config.Path()))
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "print the clones as JSON instead of opening the list")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("gh-fleet", version.Version)
		return nil
	}
	if fs.NArg() > 1 {
		return errors.New("one owner at most")
	}

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
