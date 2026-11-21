package model

import "time"

// Result is how the last attempt to read a value went.
type Result int

const (
	// NotTried is a value never asked for.
	NotTried Result = iota
	// OK is a value read.
	OK
	// Denied is a value GitHub does not give this token (permissions, SSO,
	// a feature turned off); Why says which. Not zero.
	Denied
	// Failed is an attempt that went wrong (network, timeout); Why says
	// how. The value read before, if any, is kept.
	Failed
)

// Read is a value with the last value read and the last attempt, kept
// apart: a first attempt that failed has no value, and a value read once
// stays shown when a later attempt fails or is refused.
type Read[T any] struct {
	Value T
	Has   bool      // Value was read once
	At    time.Time // when Value was read
	Last  Result
	Why   string
	Tried time.Time // when the last attempt ended
}

// Set records a value read at t.
func (r *Read[T]) Set(v T, t time.Time) {
	r.Value, r.Has, r.At, r.Last, r.Why, r.Tried = v, true, t, OK, "", t
}

// Fail records an attempt that went wrong at t, keeping the value read
// before.
func (r *Read[T]) Fail(why string, t time.Time) { r.Last, r.Why, r.Tried = Failed, why, t }

// Deny records that GitHub does not give the value, keeping the value read
// before.
func (r *Read[T]) Deny(why string, t time.Time) { r.Last, r.Why, r.Tried = Denied, why, t }

// OlderThan reports whether the value was read before now-d, or never.
func (r Read[T]) OlderThan(d time.Duration, now time.Time) bool {
	return !r.Has || now.Sub(r.At) > d
}

// Remote is what an owner's list says of a repository.
type Remote struct {
	Key           RemoteKey
	Name          string
	Fork          bool
	Archived      bool
	Private       bool
	PushedAt      time.Time
	DefaultBranch string
	Description   string
	Stars         int
}

// Heavy is what GitHub says of a repository when asked by name, beyond the
// list. Which fields are set depends on what was asked for.
type Heavy struct {
	Key        RemoteKey // the name GitHub answered with; differs for a repository renamed since
	Found      bool
	PushedAt   time.Time
	Default    string
	DefaultOid string
	CI         string
	Issues     int
	PRs        int
	Stars      int
	// ID is GitHub's number for the repository, which a rename or a move
	// keeps; IssuesNewest and PRsNewest are when the newest open issues
	// and pull requests were made, the newest first (NeedNewest).
	ID                      int64
	IssuesNewest, PRsNewest []time.Time
	// Alerts counts open Dependabot alerts; AlertsOff says they are not
	// turned on, AlertsDenied that the token may not read them.
	Alerts       int
	AlertsOff    bool
	AlertsDenied bool
	// Fields says which of the fields beyond the default branch this read
	// asked for: one the screen needs and this read lacks is not known.
	Fields Need
}

// Ahead is whether GitHub's default branch has commits a clone has not
// fetched, decided for a pair of commits: GitHub's, and the clone's
// origin/<default> when it was decided.
type Ahead struct {
	Remote, Local string
	Ahead         bool
}

// Row is one row of an owner's table: a clone, a repository on GitHub, or
// both, matched by the clone's origin. A row is found by its clone's key,
// or by its remote key when it has no clone.
type Row struct {
	Clone  CloneKey  // empty for a repository not cloned
	Remote RemoteKey // empty for a clone without a GitHub origin; GitHub's name for it, after a rename
	// Origin is the repository the clone's origin names, kept apart from
	// Remote so that reading the clone again does not undo a rename found.
	Origin RemoteKey

	Local  Read[Clone]
	Listed Read[Remote] // from the owner's list
	Heavy  Read[Heavy]  // asked for by name
	Ahead  Read[Ahead]
	// Facts are what judging the clone's linked worktrees needs: which
	// branches its default branch has, and which were worked on. Read only
	// for a clone with linked worktrees.
	Facts Read[CleanupFacts]

	// Op is what is being done to the clone, or what was done, until the
	// user puts the results away. Not kept between starts.
	Op Op `json:"-"`
	// New counts the open issues and pull requests made since the user
	// last saw the repository, worked out when the owner is handed out.
	// Not kept: it follows what was seen.
	New New `json:"-"`
}

// New is how many open issues and pull requests are new. Known is false
// when it cannot be told: the creation times were not read, or there is
// no time seen to tell them by. More says there may be more than counted
// (every one read was new). Since is the time seen they were counted
// against, for the pane to mark the titles by.
type New struct {
	Known               bool
	Issues, PRs         int
	MoreIssues, MorePRs bool
	Since               time.Time
}

// Op is a change being made to a clone or its result.
type Op struct {
	Doing  string // "fetching", "updating main", ...; empty when done
	Result string // what happened, once done
	Failed bool
	// Skipped says nothing was changed, and Result says why.
	Skipped bool
}

// Key is the row's key: its clone's, or its remote's without a clone.
func (r Row) Key() string {
	if r.Clone != "" {
		return string(r.Clone)
	}
	return string(r.Remote)
}

// Completeness says how much of an owner's repositories its list holds.
type Completeness int

const (
	ListUnknown    Completeness = iota // not read yet
	ListComplete                       // every repository the token may see
	ListPartial                        // some; Why says why the rest are missing
	ListUnreadable                     // none; Why says why
)

// Owner is a copy of what gh-tidy holds of one owner, made for the screen:
// it is never changed after it is handed out.
type Owner struct {
	Host, Login string
	// All is the view across every owner: clones only, GitHub values as
	// each owner last read them.
	All bool

	List    Completeness
	ListWhy string
	ListAt  time.Time // when the list was last read whole; zero when never
	// FromCache says what is shown came from the cache and has not been
	// read again yet in this run.
	FromCache bool

	Rows []Row

	// Reading says what is being read now, for the header; empty when
	// nothing is.
	Reading string
	Version int
}

// Need is what the columns and preview sections on the screen need read
// from GitHub, beyond what every clone needs (its default branch's name
// and commit, for MAIN and for GitHub being ahead). The screen hands the
// union of what it shows to the engine, which reads only that.
type Need uint8

const (
	NeedCI     Need = 1 << iota // the checks on the default branch
	NeedCounts                  // open issues and pull requests
	NeedAlerts                  // open Dependabot alerts
	// NeedNotCloned reads the needed fields of repositories not cloned too,
	// as the github columns do.
	NeedNotCloned
	// NeedNewest: when the newest open issues and pull requests were made,
	// to tell which are new.
	NeedNewest
)

// Has reports whether n includes any of m; All whether it includes each.
func (n Need) Has(m Need) bool { return n&m != 0 }

// All reports whether n includes every one of m.
func (n Need) All(m Need) bool { return n&m == m }
