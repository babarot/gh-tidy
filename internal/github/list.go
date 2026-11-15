package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/logging"
	"github.com/babarot/gh-tidy/internal/model"
)

// Who an owner is to the viewer, which decides which list of repositories
// may be read and how complete it is.
type OwnerKind int

const (
	OwnerViewer OwnerKind = iota // the account gh is signed in as: private repositories included
	OwnerOrg                     // an organization: what the token may see of it
	OwnerUser                    // another user: public repositories only
)

// Completeness says how much of an owner's repositories a list holds.
type Completeness int

const (
	// Unknown is a list not read: the zero value, so that a list returned
	// with an error never claims to be complete.
	Unknown Completeness = iota
	// Complete is every repository the token may see.
	Complete
	// Partial is some of them; Reason says why the rest are missing. A
	// repository not in a partial list may still exist.
	Partial
	// Unreadable is none: the list could not be read at all; Reason says why.
	Unreadable
)

// ListedRepo is one repository of an owner's list.
type ListedRepo struct {
	Key           model.RemoteKey
	Name          string
	NameWithOwner string
	Fork          bool
	Archived      bool
	Private       bool
	PushedAt      time.Time
	DefaultBranch string
	Description   string
}

// Listing is an owner's list as read.
type Listing struct {
	// Unchanged says every page answered 304 to the ETags given: the list
	// read before still holds, and Repos is empty.
	Unchanged    bool
	Repos        []ListedRepo
	Pages        Pages // for the next read
	Completeness Completeness
	Reason       string
	// Rate is the rate limit the last response reported; Cost counts the
	// pages that were not answered with 304.
	Rate Rate
}

// Pages is what a list read before leaves for asking it again
// conditionally: each page's ETag, whether the read ended on a short page
// (so the last ETag also covers repositories added at the end), and how
// complete the list was, which an unchanged answer carries forward.
type Pages struct {
	ETags        []string
	Ended        bool
	Completeness Completeness
	Reason       string
}

const perPage = 100

// List reads owner's repositories, of kind. With the pages of a list read
// before, it asks each page conditionally: when every page answers 304 the
// list has not changed, at no cost to the rate limit. When any page has
// changed, every page is read again, so that a list is never put together
// from pages read at different times. At most maxPages pages are read; a
// longer list is Partial.
func (c *Client) List(ctx context.Context, owner string, kind OwnerKind, prev Pages, maxPages int) (Listing, error) {
	l, err := c.list(ctx, owner, kind, prev, maxPages)
	if err != nil {
		l.Completeness, l.Repos = Unknown, nil
	}
	return l, err
}

func (c *Client) list(ctx context.Context, owner string, kind OwnerKind, prev Pages, maxPages int) (Listing, error) {
	// A list cut off at the page limit, asked for with more pages now, is
	// read whole: its pages answering 304 would say nothing of the rest.
	if len(prev.ETags) > 0 && (prev.Ended || maxPages <= len(prev.ETags)) {
		l, changed, err := c.listPages(ctx, owner, kind, prev, maxPages)
		if err != nil || !changed {
			return l, err
		}
		cost := l.Rate.Cost
		l, _, err = c.listPages(ctx, owner, kind, Pages{}, maxPages)
		l.Rate.Cost += cost
		return l, err
	}
	l, _, err := c.listPages(ctx, owner, kind, Pages{}, maxPages)
	return l, err
}

// listPages reads the pages, conditionally when etags are given, and
// reports whether any page changed. A conditional pass stops at the first
// changed page.
func (c *Client) listPages(ctx context.Context, owner string, kind OwnerKind, prev Pages, maxPages int) (Listing, bool, error) {
	l := Listing{Completeness: Complete}
	seen := map[model.RemoteKey]bool{}
	etags := prev.ETags
	conditional := len(etags) > 0
	for page := 1; ; page++ {
		if page > maxPages {
			l.Completeness = Partial
			l.Reason = fmt.Sprintf("more than %d repositories; read the rest when asked", maxPages*perPage)
			l.Pages.Completeness, l.Pages.Reason = l.Completeness, l.Reason
			return l, true, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.restBase+listPath(url.PathEscape(owner), kind, page), nil)
		if err != nil {
			return l, false, err
		}
		if conditional && page <= len(etags) {
			req.Header.Set("If-None-Match", etags[page-1])
		}
		resp, rate, err := c.do(req, "list")
		cost := l.Rate.Cost
		l.Rate = rate
		l.Rate.Cost = cost
		if err != nil {
			var se *StatusError
			switch {
			case errors.As(err, &se) && se.Status == http.StatusForbidden && se.SSO != "":
				return Listing{Completeness: Unreadable, Reason: "this token is not authorized for the organization's SSO", Rate: rate}, true, nil
			case errors.As(err, &se) && se.Status == http.StatusNotFound:
				return l, false, ErrNoOwner
			}
			return l, false, err
		}
		etag := resp.Header.Get("Etag")
		if resp.StatusCode == http.StatusNotModified {
			_ = resp.Body.Close()
			l.Pages.ETags = append(l.Pages.ETags, etag)
			if page < len(etags) {
				continue
			}
			l.Unchanged = true
			l.Pages.Ended, l.Pages.Completeness, l.Pages.Reason = prev.Ended, prev.Completeness, prev.Reason
			l.Completeness, l.Reason = prev.Completeness, prev.Reason
			return l, false, nil
		}
		if conditional {
			_ = resp.Body.Close()
			rate.Cost = l.Rate.Cost + 1           // this changed page cost a point
			return Listing{Rate: rate}, true, nil // read every page again
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			return l, false, ErrNoOwner
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
			return l, false, &StatusError{Status: resp.StatusCode, Message: message(b)}
		}
		var repos []restRepo
		err = json.NewDecoder(resp.Body).Decode(&repos)
		partial := strings.Contains(resp.Header.Get("X-Github-Sso"), "partial-results")
		_ = resp.Body.Close()
		if err != nil {
			return l, false, err
		}
		l.Rate.Cost++
		l.Pages.ETags = append(l.Pages.ETags, etag)
		for _, r := range repos {
			// A repository added or removed while the pages are read moves
			// the others across pages; a repository is listed once.
			lr := r.listed(c.host)
			if !seen[lr.Key] {
				seen[lr.Key] = true
				l.Repos = append(l.Repos, lr)
			}
		}
		if partial {
			l.Completeness, l.Reason = Partial, "this token is not authorized for the organization's SSO; only some repositories came back"
		}
		if len(repos) < perPage {
			if kind == OwnerUser && l.Completeness == Complete {
				l.Completeness, l.Reason = Partial, "another user's public repositories only"
			}
			l.Pages.Ended, l.Pages.Completeness, l.Pages.Reason = true, l.Completeness, l.Reason
			return l, true, nil
		}
	}
}

func listPath(owner string, kind OwnerKind, page int) string {
	q := fmt.Sprintf("per_page=%d&page=%d", perPage, page)
	switch kind {
	case OwnerViewer:
		return "user/repos?affiliation=owner&" + q
	case OwnerOrg:
		return "orgs/" + owner + "/repos?type=all&" + q
	}
	return "users/" + owner + "/repos?type=owner&" + q
}

type restRepo struct {
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Fork          bool      `json:"fork"`
	Archived      bool      `json:"archived"`
	Private       bool      `json:"private"`
	PushedAt      time.Time `json:"pushed_at"`
	DefaultBranch string    `json:"default_branch"`
	Description   string    `json:"description"`
}

func (r restRepo) listed(host string) ListedRepo {
	return ListedRepo{Key: model.NewRemoteKey(host, r.FullName), Name: r.Name, NameWithOwner: r.FullName, Fork: r.Fork, Archived: r.Archived,
		Private: r.Private, PushedAt: r.PushedAt, DefaultBranch: r.DefaultBranch, Description: r.Description}
}

// Viewer is who gh is signed in as, and the organizations it belongs to.
// OrgsIncomplete says GitHub left some organizations out (an error beside
// the login, as for one whose SSO the token is not authorized for): an
// organization missing from Orgs then reads as another user's, whose list
// is partial.
type Viewer struct {
	Login          string
	Orgs           []string
	OrgsIncomplete bool
}

// Viewer reads the account and its organizations (one GraphQL query).
func (c *Client) Viewer(ctx context.Context) (Viewer, Rate, error) {
	var d struct {
		Viewer struct {
			Login         string
			Organizations struct{ Nodes []struct{ Login string } }
		}
		RateLimit struct{ Cost int }
	}
	errs, rate, err := c.graphql(ctx, "viewer", `query { viewer { login organizations(first: 100) { nodes { login } } } rateLimit { cost } }`, &d)
	if err != nil {
		return Viewer{}, rate, err
	}
	// Without the viewer, every owner would read as another user's.
	if d.Viewer.Login == "" {
		msg := "GitHub did not say who is signed in"
		if len(errs) > 0 {
			msg += ": " + errs[0].Message
		}
		return Viewer{}, rate, errors.New(msg)
	}
	rate.Cost = d.RateLimit.Cost
	v := Viewer{Login: d.Viewer.Login, OrgsIncomplete: len(errs) > 0}
	if v.OrgsIncomplete {
		c.log.Info("github viewer: some organizations left out", logging.KeyReason, errs[0].Type)
	}
	for _, o := range d.Viewer.Organizations.Nodes {
		v.Orgs = append(v.Orgs, o.Login)
	}
	return v, rate, nil
}

// KindOf tells who owner is to the viewer.
func (v Viewer) KindOf(owner string) OwnerKind {
	switch {
	case strings.EqualFold(owner, v.Login):
		return OwnerViewer
	case containsFold(v.Orgs, owner):
		return OwnerOrg
	}
	return OwnerUser
}

func containsFold(ss []string, s string) bool {
	for _, x := range ss {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
