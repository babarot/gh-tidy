package app

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/babarot/gh-tidy/internal/command"
	"github.com/babarot/gh-tidy/internal/config"
	"github.com/babarot/gh-tidy/internal/local"
	"github.com/babarot/gh-tidy/internal/model"
)

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

// changer changes clones with git, and removes worktrees with the
// command the settings name.
type changer struct {
	remove []string // [cleanup] worktree_remove
}

func (changer) Fetch(ctx context.Context, path string) error { return local.Fetch(ctx, path) }
func (changer) AdvanceFacts(ctx context.Context, path, name string, src model.DefaultSource) model.AdvanceFacts {
	return local.AdvanceFacts(ctx, path, name, src)
}
func (changer) FastForwardRef(ctx context.Context, path, name string) error {
	return local.FastForwardRef(ctx, path, name)
}
func (changer) MergeFastForward(ctx context.Context, wt, name string) error {
	return local.MergeFastForward(ctx, wt, name)
}
func (changer) CleanupFacts(ctx context.Context, c model.Clone) (model.CleanupFacts, error) {
	return local.CleanupFacts(ctx, c)
}
func (changer) WorktreeDirty(ctx context.Context, wt string) (int, error) {
	return local.WorktreeDirty(ctx, wt)
}
func (changer) BranchExists(ctx context.Context, path, branch string) bool {
	return local.BranchExists(ctx, path, branch)
}
func (changer) DeleteMergedBranch(ctx context.Context, path, branch, base string) error {
	return local.DeleteMergedBranch(ctx, path, branch, base)
}
func (changer) DeleteBranch(ctx context.Context, path, branch string) error {
	return local.DeleteBranch(ctx, path, branch)
}
func (changer) PruneWorktrees(ctx context.Context, path string) error {
	return local.PruneWorktrees(ctx, path)
}

// RemoveWorktree runs worktree_remove in the clone, with {path} the
// worktree and the clone's names for the other placeholders.
func (c changer) RemoveWorktree(ctx context.Context, path, wt, branch string) error {
	cl := local.Scan(ctx, path)
	v := map[string]string{"path": wt, "branch": branch, "default": cl.Default, "name": cl.Name}
	if cl.Slug != "" {
		v["repo"], v["url"] = cl.Slug, "https://"+cl.Host+"/"+cl.Slug
		if o, n, ok := strings.Cut(cl.Slug, "/"); ok {
			v["owner"], v["name"] = o, n
		}
	}
	_, err := command.Run(ctx, path, config.Expand(c.remove, v))
	return err
}

// cloner clones with gh.
type cloner struct{}

func (cloner) Clone(ctx context.Context, host, slug, dir string) error {
	return command.Clone(ctx, host, slug, dir)
}

// programs are the programs other than git the screen runs.
type programs struct{}

func (programs) Run(ctx context.Context, dir string, argv []string) (string, error) {
	return command.Run(ctx, dir, argv)
}
func (programs) Interactive(dir string, argv []string) tea.ExecCommand {
	return command.NewInteractive(dir, argv)
}
func (programs) OpenURL(u string) error { return command.OpenURL(u) }
func (programs) Reveal(p string) error  { return command.Reveal(p) }
func (programs) Copy(t string) error    { return command.Copy(t) }
