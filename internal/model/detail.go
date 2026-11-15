package model

import "time"

// Detail is what the preview pane shows behind a row's marks: the commits
// p would bring in, those not pushed, those on the default branch the
// branch lacks, the files changed and the stash. It is read for the row
// selected only, and not kept between starts.
type Detail struct {
	// Incoming are the commits on origin's default branch the local one
	// lacks; for a clone, not a worktree.
	Incoming []Commit
	// Unpushed are the commits of the branch its upstream lacks or, with
	// no upstream (NeverPushed), those the default branch lacks.
	Unpushed    []Commit
	NeverPushed bool
	// BehindMain are the commits on the default branch the branch lacks.
	BehindMain []Commit
	Files      []File
	// Stash is the clone's stash, for a clone, not a worktree.
	Stash []Stash
	Err   string
}

// Commit is a commit as the preview lists it.
type Commit struct {
	Hash    string // short
	Subject string
	Author  string
	When    time.Time
}

// File is a changed file, with its two-letter status as git status
// --short writes it ("M ", " M", "??").
type File struct {
	XY   string
	Path string
}

// Stash is one entry of the stash.
type Stash struct {
	Ref     string // stash@{0}
	Subject string
	When    time.Time
}
