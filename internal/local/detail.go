package local

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/gitx"
	"github.com/babarot/gh-tidy/internal/model"
)

// detailMax is how many commits, files or stash entries one part of the
// preview lists; the row's counts say how many there are in all.
const detailMax = 50

// Detail reads what the preview shows of the clone c or, when worktree is
// a linked worktree's path, of that worktree. A part that cannot be read
// is left empty and the first error is kept.
func Detail(ctx context.Context, c model.Clone, worktree string) model.Detail {
	var d model.Detail
	keep := func(err error) {
		if err != nil && d.Err == "" {
			d.Err = err.Error()
		}
	}
	dir, branch := c.Path, c.Head
	if worktree != "" {
		dir, branch = worktree, ""
		for _, w := range c.Worktrees {
			if w.Path == worktree {
				branch = w.Holds()
			}
		}
	}
	base := ""
	if c.Default != "" {
		base = "refs/remotes/origin/" + c.Default
		if !refExists(ctx, c.Path, base) {
			base = "refs/heads/" + c.Default
			if !refExists(ctx, c.Path, base) {
				base = ""
			}
		}
	}
	var err error
	if worktree == "" && c.Default != "" && refExists(ctx, c.Path, "refs/heads/"+c.Default) && refExists(ctx, c.Path, "refs/remotes/origin/"+c.Default) {
		d.Incoming, err = commits(ctx, c.Path, "refs/heads/"+c.Default+"..refs/remotes/origin/"+c.Default)
		keep(err)
	}
	if branch != "" {
		up := upstreamOf(c, branch)
		switch {
		case up != "":
			d.Unpushed, err = commits(ctx, c.Path, "refs/remotes/"+up+"..refs/heads/"+branch)
			keep(err)
		case branch != c.Default && base != "":
			d.NeverPushed = true
			d.Unpushed, err = commits(ctx, c.Path, base+"..refs/heads/"+branch)
			keep(err)
		}
		if branch != c.Default && base != "" {
			d.BehindMain, err = commits(ctx, c.Path, "refs/heads/"+branch+".."+base)
			keep(err)
		}
	}
	d.Files, err = files(ctx, dir)
	keep(err)
	if worktree == "" {
		d.Stash, err = stash(ctx, c.Path)
		keep(err)
	}
	return d
}

// upstreamOf is the remote-tracking ref branch tracks, as origin/x, when it
// exists; empty without one.
func upstreamOf(c model.Clone, branch string) string {
	for _, b := range c.Branches {
		if b.Name == branch && !b.Gone {
			return b.Upstream
		}
	}
	return ""
}

func refExists(ctx context.Context, dir, ref string) bool {
	_, err := gitx.Run(ctx, dir, "rev-parse", "-q", "--verify", ref)
	return err == nil
}

func commits(ctx context.Context, dir, rng string) ([]model.Commit, error) {
	out, err := gitx.Run(ctx, dir, "log", "-n", strconv.Itoa(detailMax), "--format=%h%x00%s%x00%an%x00%ct", rng, "--")
	if err != nil {
		return nil, err
	}
	var cs []model.Commit
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.Split(l, "\x00")
		if len(f) != 4 {
			continue
		}
		cs = append(cs, model.Commit{Hash: f[0], Subject: f[1], Author: f[2], When: unixText(f[3])})
	}
	return cs, nil
}

func files(ctx context.Context, dir string) ([]model.File, error) {
	out, err := gitx.Run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var fs []model.File
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts) && len(fs) < detailMax; i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		xy := p[:2]
		fs = append(fs, model.File{XY: xy, Path: p[3:]})
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the path it was renamed or copied from
		}
	}
	return fs, nil
}

func stash(ctx context.Context, dir string) ([]model.Stash, error) {
	out, err := gitx.Run(ctx, dir, "stash", "list", "-n", strconv.Itoa(detailMax), "--format=%gd%x00%gs%x00%ct")
	if err != nil {
		return nil, err
	}
	var ss []model.Stash
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.Split(l, "\x00")
		if len(f) != 3 {
			continue
		}
		ss = append(ss, model.Stash{Ref: f[0], Subject: f[1], When: unixText(f[2])})
	}
	return ss, nil
}

func unixText(s string) time.Time {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return unix(n)
}
