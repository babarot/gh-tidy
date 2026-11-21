package app

import (
	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/engine"
	"github.com/babarot/gh-tidy/internal/tui"
)

// Parts are the concrete pieces gh tidy is built from: the clones on
// disk, the changes to them, cloning, and the programs other than git the
// screen runs. A program that runs gh tidy against a world of its own (the
// demo) takes them from here, so that it runs what gh tidy runs; gh tidy
// itself has no way to run against made-up data (ADR-010).
type Parts struct {
	Local    engine.Local
	Change   engine.Changer
	Cloner   engine.Cloner
	Commands tui.Commands
}

// NewParts returns the pieces, worktrees removed as cfg says.
func NewParts(cfg config.File) Parts {
	return Parts{Local: disk{}, Change: changer{remove: cfg.Cleanup.WorktreeRemove}, Cloner: cloner{}, Commands: programs{}}
}
