package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
)

// detailFirst is how many alerts and checks one query reads: GitHub sorts
// neither by severity nor by state, so they are sorted and filtered here
// from as many as a page holds.
const detailFirst = 100

// DetailWant is what to read of a repository's detail: its open
// Dependabot alerts, and the checks on the commit Oid (empty for none).
type DetailWant struct {
	Alerts bool
	Oid    string
	// Titles asks for the open issues and pull requests, the newest first.
	Titles bool
}

// titlesFirst is how many open issues and pull requests the pane lists.
const titlesFirst = 10

// RepoDetail reads the open Dependabot alerts and the checks of one
// repository, each part only when want asks for it. A part GitHub refuses
// or fails on says so in its own result; the other part stands.
func (c *Client) RepoDetail(ctx context.Context, slug string, want DetailWant) (model.RepoDetail, Rate, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var parts []string
	if want.Alerts {
		parts = append(parts, fmt.Sprintf("vulnerabilityAlerts(states: OPEN, first: %d) { totalCount nodes { vulnerableManifestPath "+
			"securityVulnerability { severity vulnerableVersionRange package { name ecosystem } firstPatchedVersion { identifier } } } }", detailFirst))
	}
	if want.Oid != "" {
		parts = append(parts, fmt.Sprintf("object(oid: %s) { ... on Commit { statusCheckRollup { state contexts(first: %d) { "+
			"checkRunCountsByState { state count } statusContextCountsByState { state count } "+
			"nodes { __typename ... on CheckRun { name status conclusion } ... on StatusContext { context state } } } } } }", quote(want.Oid), detailFirst))
	}
	if want.Titles {
		order := fmt.Sprintf("states: OPEN, first: %d, orderBy: {field: CREATED_AT, direction: DESC}", titlesFirst)
		items := "nodes { number title createdAt author { login } }"
		parts = append(parts, "issues("+order+") { "+items+" }", "pullRequests("+order+") { "+items+" }")
	}
	var d model.RepoDetail
	if len(parts) == 0 {
		return d, Rate{}, nil
	}
	q := fmt.Sprintf("query { repository(owner: %s, name: %s) { %s } rateLimit { cost } }", quote(owner), quote(name), strings.Join(parts, " "))
	var data struct {
		Repository *struct {
			VulnerabilityAlerts *struct {
				TotalCount int
				Nodes      []struct {
					VulnerableManifestPath string
					SecurityVulnerability  struct {
						Severity               string
						VulnerableVersionRange string
						Package                struct{ Name, Ecosystem string }
						FirstPatchedVersion    *struct{ Identifier string }
					}
				}
			}
			Issues, PullRequests *struct {
				Nodes []struct {
					Number    int
					Title     string
					CreatedAt time.Time
					Author    *struct{ Login string }
				}
			}
			Object *struct {
				StatusCheckRollup *struct {
					State    string
					Contexts struct {
						CheckRunCountsByState []struct {
							State string
							Count int
						}
						StatusContextCountsByState []struct {
							State string
							Count int
						}
						Nodes []struct {
							Typename   string `json:"__typename"`
							Name       string
							Status     string
							Conclusion string
							Context    string
							State      string
						}
					}
				}
			}
		}
		RateLimit struct{ Cost int }
	}
	errs, rate, err := c.graphql(ctx, "detail", q, &data)
	if err != nil {
		return d, rate, err
	}
	rate.Cost = data.RateLimit.Cost
	c.log.Debug("github detail", logging.KeyRepo, slug, logging.KeyGQLCost, rate.Cost, logging.KeyGQLRemaining, rate.Remaining)
	d.AlertsAsked, d.ChecksAsked, d.TitlesAsked = want.Alerts, want.Oid != "", want.Titles
	issuesErr, prsErr := false, false
	for _, e := range errs {
		field := ""
		if len(e.Path) > 1 {
			field, _ = e.Path[1].(string)
		}
		switch {
		case len(e.Path) == 1 && e.Type == "NOT_FOUND":
			return d, rate, &StatusError{Status: 404, Message: "the repository is not visible to this token"}
		case field == "vulnerabilityAlerts":
			d.AlertsDenied = true
		case field == "object":
			d.ChecksErr = e.Message
		case field == "issues":
			issuesErr = true
		case field == "pullRequests":
			prsErr = true
		default:
			return d, rate, &StatusError{Status: 200, Message: e.Message}
		}
	}
	r := data.Repository
	if r == nil {
		return d, rate, &StatusError{Status: 200, Message: "GitHub did not answer for the repository"}
	}
	if want.Alerts && !d.AlertsDenied {
		if a := r.VulnerabilityAlerts; a == nil {
			d.AlertsDenied = true
		} else {
			d.AlertsTotal = a.TotalCount
			for _, n := range a.Nodes {
				v := n.SecurityVulnerability
				al := model.Alert{Package: v.Package.Name, Ecosystem: strings.ToLower(v.Package.Ecosystem), Severity: strings.ToLower(v.Severity),
					Range: v.VulnerableVersionRange, Manifest: n.VulnerableManifestPath}
				if v.FirstPatchedVersion != nil {
					al.Fixed = v.FirstPatchedVersion.Identifier
				}
				d.Alerts = append(d.Alerts, al)
			}
			model.SortAlerts(d.Alerts)
		}
	}
	if want.Oid != "" && d.ChecksErr == "" && r.Object != nil && r.Object.StatusCheckRollup != nil {
		roll := r.Object.StatusCheckRollup
		for _, s := range roll.Contexts.CheckRunCountsByState {
			switch {
			case failedConclusion(s.State):
				d.ChecksFailed += s.Count
			case s.State == "QUEUED" || s.State == "IN_PROGRESS" || s.State == "PENDING" || s.State == "WAITING" || s.State == "REQUESTED":
				d.ChecksRunning += s.Count
			}
		}
		for _, s := range roll.Contexts.StatusContextCountsByState {
			switch s.State {
			case "FAILURE", "ERROR":
				d.ChecksFailed += s.Count
			case "PENDING", "EXPECTED":
				d.ChecksRunning += s.Count
			}
		}
		for _, n := range roll.Contexts.Nodes {
			var ch model.Check
			switch n.Typename {
			case "CheckRun":
				ch.Name = n.Name
				switch {
				case n.Status != "COMPLETED":
					ch.Running = true
				case failedConclusion(n.Conclusion):
				default:
					continue
				}
			case "StatusContext":
				ch.Name = n.Context
				switch n.State {
				case "FAILURE", "ERROR":
				case "PENDING", "EXPECTED":
					ch.Running = true
				default:
					continue
				}
			default:
				continue
			}
			d.Checks = append(d.Checks, ch)
		}
	}
	if want.Titles {
		opened := func(l *struct {
			Nodes []struct {
				Number    int
				Title     string
				CreatedAt time.Time
				Author    *struct{ Login string }
			}
		}) []model.Opened {
			var out []model.Opened
			for _, n := range l.Nodes {
				o := model.Opened{Number: n.Number, Title: n.Title, Created: n.CreatedAt}
				if n.Author != nil {
					o.Author = n.Author.Login
				}
				out = append(out, o)
			}
			return out
		}
		if !issuesErr && r.Issues != nil {
			d.Issues, d.IssuesRead = opened(r.Issues), true
		}
		if !prsErr && r.PullRequests != nil {
			d.PRs, d.PRsRead = opened(r.PullRequests), true
		}
	}
	return d, rate, nil
}

// failedConclusion reports whether a check run's conclusion makes GitHub
// call the commit's checks failed.
func failedConclusion(c string) bool {
	switch c {
	case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE": //nolint:misspell // GitHub's own spelling of the state
		return true
	}
	return false
}
