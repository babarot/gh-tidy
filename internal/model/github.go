package model

import (
	"slices"
	"time"
)

// RepoDetail is what the preview of the github columns shows beyond the
// counts: a repository's open Dependabot alerts and the checks that failed
// or run on its default branch's head. Each part says whether it was
// asked for, and whether GitHub refused or failed it.
type RepoDetail struct {
	AlertsAsked  bool
	Alerts       []Alert // the most severe first
	AlertsTotal  int     // all the open alerts, beyond those read
	AlertsDenied bool    // this token may not read them

	ChecksAsked                 bool
	Checks                      []Check // failed and running, as GitHub lists them
	ChecksFailed, ChecksRunning int     // counted by GitHub, beyond those read
	ChecksErr                   string
}

// Alert is one open Dependabot alert.
type Alert struct {
	Package, Ecosystem string
	Severity           string // critical, high, moderate, low
	Range              string // the versions it affects
	Fixed              string // the first version that fixes it; empty for none yet
	Manifest           string
}

// Check is a check that failed, or runs.
type Check struct {
	Name    string
	Running bool
}

// severities rank the alerts, the most severe first.
var severities = map[string]int{"critical": 0, "high": 1, "moderate": 2, "medium": 2, "low": 3}

func severity(s string) int {
	if n, ok := severities[s]; ok {
		return n
	}
	return len(severities)
}

// SortAlerts puts the most severe alerts first, in the order GitHub gave
// within one severity.
func SortAlerts(as []Alert) {
	slices.SortStableFunc(as, func(a, b Alert) int { return severity(a.Severity) - severity(b.Severity) })
}

// GitHubDetail is how the detail of a repository stands, for the screen.
type GitHubDetail struct {
	State GitHubDetailState
	Value RepoDetail
	// Why says why it is not read, when it was held back or failed.
	Why string
	At  time.Time
}

// GitHubDetailState is where the read of a repository's detail is.
type GitHubDetailState int

const (
	// DetailNone: nothing to read, as no count asks for it, or not here
	// (all reads no more of GitHub).
	DetailNone GitHubDetailState = iota
	DetailReading
	DetailRead
	DetailHeld
	DetailFailed
	// DetailAll: in all, which reads no more of GitHub.
	DetailAll
)
