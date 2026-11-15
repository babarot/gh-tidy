package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

// Want says which of a repository's heavier fields to read: only what the
// columns shown need, since each field adds to how long GitHub takes.
type Want struct {
	Default bool // the default branch's name and commit
	CI      bool // the state of the checks on the default branch's commit
	Counts  bool // open issues and pull requests
	Alerts  bool // open Dependabot alerts, and whether they are turned on
}

// HeavyRepo is what GitHub said of one repository asked for by name.
type HeavyRepo struct {
	// Asked is the name it was asked for, and Key the name GitHub answered
	// with: they differ for a repository renamed or moved since.
	Asked, Key model.RemoteKey
	// Found is false when GitHub has no repository by that name that the
	// token may see: deleted, moved away, or not readable. These cannot be
	// told apart.
	Found      bool
	PushedAt   time.Time
	Default    string // empty for an empty repository
	DefaultOid string
	CI         string // SUCCESS, FAILURE, PENDING, ERROR, EXPECTED; empty with no checks
	Issues     int
	PRs        int
	// Alerts is the count of open Dependabot alerts. AlertsOff says they are
	// not turned on for the repository; AlertsDenied that the token may not
	// read them. Neither is zero.
	Alerts       int
	AlertsOff    bool
	AlertsDenied bool
	// Err is another error GitHub reported for this repository.
	Err string
}

// Heavy reads the fields want asks for of the repositories named by slugs
// (owner/name), in one query of aliases. The caller keeps the batch small:
// GitHub cuts off a query it takes more than ten seconds over (ErrTimeout).
func (c *Client) Heavy(ctx context.Context, slugs []string, want Want) ([]HeavyRepo, Rate, error) {
	fields := []string{"nameWithOwner", "pushedAt"}
	if want.Default || want.CI {
		ci := ""
		if want.CI {
			ci = " ... on Commit { statusCheckRollup { state } }"
		}
		fields = append(fields, "defaultBranchRef { name target { oid"+ci+" } }")
	}
	if want.Counts {
		fields = append(fields, "issues(states: OPEN) { totalCount }", "pullRequests(states: OPEN) { totalCount }")
	}
	if want.Alerts {
		fields = append(fields, "hasVulnerabilityAlertsEnabled", "vulnerabilityAlerts(states: OPEN) { totalCount }")
	}
	var q strings.Builder
	q.WriteString("query {")
	for i, s := range slugs {
		owner, name, _ := strings.Cut(s, "/")
		fmt.Fprintf(&q, " r%d: repository(owner: %s, name: %s) { %s }", i, quote(owner), quote(name), strings.Join(fields, " "))
	}
	q.WriteString(" rateLimit { cost } }")

	var data map[string]json.RawMessage
	errs, rate, err := c.graphql(ctx, "heavy", q.String(), &data)
	if err != nil {
		return nil, rate, err
	}
	if raw, ok := data["rateLimit"]; ok {
		var rl struct{ Cost int }
		if json.Unmarshal(raw, &rl) == nil {
			rate.Cost = rl.Cost
		}
	}
	out := make([]HeavyRepo, len(slugs))
	for i, s := range slugs {
		h := &out[i]
		h.Asked = model.NewRemoteKey(c.host, s)
		alias := fmt.Sprintf("r%d", i)
		var n *node2
		if raw, ok := data[alias]; ok && string(raw) != "null" {
			if err := json.Unmarshal(raw, &n); err != nil {
				h.Err = err.Error()
				continue
			}
		}
		for _, e := range errs {
			if len(e.Path) == 0 || e.Path[0] != alias {
				continue
			}
			switch {
			case len(e.Path) == 1 && e.Type == "NOT_FOUND":
			case len(e.Path) > 1 && e.Path[1] == "vulnerabilityAlerts":
				h.AlertsDenied = true
			default:
				h.Err = e.Message
			}
		}
		if n == nil {
			continue
		}
		h.Found, h.Key, h.PushedAt = true, model.NewRemoteKey(c.host, n.NameWithOwner), n.PushedAt
		if n.DefaultBranchRef != nil {
			h.Default, h.DefaultOid = n.DefaultBranchRef.Name, n.DefaultBranchRef.Target.Oid
			if r := n.DefaultBranchRef.Target.StatusCheckRollup; r != nil {
				h.CI = r.State
			}
		}
		h.Issues, h.PRs = n.Issues.TotalCount, n.PullRequests.TotalCount
		if want.Alerts && !h.AlertsDenied {
			switch {
			case n.HasVulnerabilityAlertsEnabled != nil && !*n.HasVulnerabilityAlertsEnabled:
				h.AlertsOff = true
			case n.VulnerabilityAlerts == nil:
				h.AlertsDenied = true // null without an error: not readable either
			default:
				h.Alerts = n.VulnerabilityAlerts.TotalCount
			}
		}
	}
	return out, rate, nil
}

type node2 struct {
	NameWithOwner    string
	PushedAt         time.Time
	DefaultBranchRef *struct {
		Name   string
		Target struct {
			Oid               string
			StatusCheckRollup *struct{ State string }
		}
	}
	Issues                        struct{ TotalCount int }
	PullRequests                  struct{ TotalCount int }
	HasVulnerabilityAlertsEnabled *bool
	VulnerabilityAlerts           *struct{ TotalCount int }
}

// quote writes s as a GraphQL string.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
