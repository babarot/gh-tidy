package recording

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/app"
	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/engine"
	"github.com/babarot/gh-tidy/internal/locate"
	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/store"
	"github.com/babarot/gh-tidy/internal/tui"
)

// Run runs gh tidy's engine and screen on the world, as gh tidy puts them
// together, with everything kept under the world's directory: the config
// file, the cache, the state and what was seen. Nothing the user keeps is
// read or written.
func Run(ctx context.Context, w *World) error {
	eng, cfg, roots, commands, err := w.start(ctx)
	if err != nil {
		return err
	}
	m, err := tui.New(tui.Options{Engine: eng, Commands: commands, Cwd: w.Home, Config: cfg, Owners: []string{Owner},
		Owner: Owner, Home: w.Home, Roots: roots})
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// start builds the engine on the world and starts it.
func (w *World) start(ctx context.Context) (*engine.Engine, config.File, []string, tui.Commands, error) {
	cfg, err := config.Load(w.Config)
	if err != nil {
		return nil, cfg, nil, nil, err
	}
	roots, err := locate.Roots(cfg.Core.Roots)
	if err != nil {
		return nil, cfg, nil, nil, err
	}
	st := store.Store{CacheDir: w.Home + "/cache", StateDir: w.Home + "/state"}
	// The owner was first read a day before: one issue made since is new.
	if _, err := st.SaveSeen(store.Seen{Owners: map[string]time.Time{"github.com/" + Owner: w.Built.Add(-baseline)}}); err != nil {
		return nil, cfg, nil, nil, err
	}
	parts := app.NewParts(cfg)
	eng := engine.New(engine.Options{Roots: roots, Local: parts.Local, Change: slow{parts.Change}, Cloner: parts.Cloner,
		GitHub: w.GitHub(), Store: st, Logger: logging.Discard()})
	if err := eng.Start(ctx); err != nil {
		return nil, cfg, nil, nil, err
	}
	return eng, cfg, roots, quiet{parts.Commands}, nil
}

// slow makes the changes to the clones take as long as they would against
// GitHub, so that the activity box shows them: a fetch from a remote on
// the same disk ends at once.
type slow struct{ engine.Changer }

func (s slow) Fetch(ctx context.Context, path string) error {
	pause(ctx, 500*time.Millisecond+time.Duration(len(path)%5)*120*time.Millisecond)
	return s.Changer.Fetch(ctx, path)
}

func (s slow) RemoveWorktree(ctx context.Context, path, wt, branch string) error {
	pause(ctx, 400*time.Millisecond)
	return s.Changer.RemoveWorktree(ctx, path, wt, branch)
}

func pause(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// quiet runs the programs the world needs (git for worktree_remove, the
// user's actions) and leaves the screen's outside effects undone: no
// browser opens, no Finder, and the clipboard is not written.
type quiet struct{ tui.Commands }

func (quiet) OpenURL(string) error { return nil }
func (quiet) Reveal(string) error  { return nil }
func (quiet) Copy(string) error    { return nil }
