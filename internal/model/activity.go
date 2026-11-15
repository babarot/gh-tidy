package model

import "time"

// Activity is what gh-tidy is doing, or did last, for the screen: a run is
// the work from the moment something is queued while nothing was, until
// nothing is left. It is a copy, never changed after it is handed out.
type Activity struct {
	// Verb says what the run does ("Reading", "Fetching", ...), and What
	// for whom (the owner shown when it started).
	Verb, What string
	// Counts are the run's work by kind, done out of queued, in the order
	// each kind first came. Total may grow while the run goes on.
	Counts []Count
	// Running are the tasks running, oldest first; Done the last tasks
	// done, oldest first.
	Running, Done []Task
	// Started is when the run started, Ended when it ended; Ended is zero
	// while it runs.
	Started, Ended time.Time
	// Workers are how many of each kind of outside access run, out of
	// their limits; Queued how many tasks wait.
	Workers []Workers
	Queued  int
	Rate    RateLeft
}

// Busy reports whether the run goes on.
func (a Activity) Busy() bool { return !a.Started.IsZero() && a.Ended.IsZero() }

// Count is work of a kind in a run.
type Count struct {
	Name        string // clones, GitHub, fetched, updated, cleaned, cloned
	Done, Total int
}

// Task is one piece of work. Note says how it went in a few words: never
// an error's own text, which can hold a remote's address.
type Task struct {
	Kind, Name, Note string
	Failed           bool
	Start, End       time.Time
}

// Workers is one kind of outside access.
type Workers struct {
	Name           string // disk, net, api
	Running, Limit int
}

// RateLeft is GitHub's rate limit as the screen shows it: the lower of
// what REST and GraphQL have left, which one in Resource (for the log),
// and what gh tidy spent.
type RateLeft struct {
	Known            bool
	Remaining, Limit int
	Reset            time.Time
	Resource         string
	// Used and Saved are what the reads asked for spent since the last
	// one began (opening an owner, r, L): points, and pages answered 304.
	// Unasked are what the reads gh tidy made on its own spent since.
	// Used counts what gh tidy saw; GitHub may have charged more.
	Used, Saved, UnaskedUsed, UnaskedSaved int
	// Asking says a request to GitHub's API is in flight; LastAsked is
	// when the last one ended.
	Asking    bool
	LastAsked time.Time
}
