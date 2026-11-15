package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
	// Complete is every repository the token may see.
	Complete Completeness = iota
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

// Pages is what a list read before leaves for asking again conditionally:
// each page's ETag. A read goes on until a page holds fewer than a full
// page, so the last ETag also covers repositories added at the end.
type Pages struct {
	ETags []string
}

const perPage = 100

// List reads owner's repositories, of kind. With the pages of a list read
// before, it asks each page conditionally: when every page answers 304 the
// list has not changed, at no cost to the rate limit. When any page has
// changed, every page is read again, so that a list is never put together
// from pages read at different times. At most maxPages pages are read; a
// longer list is Partial.
func (c *Client) List(ctx context.Context, owner string, kind OwnerKind, prev Pages, maxPages int) (Listing, error) {
	if len(prev.ETags) > 0 {
		l, changed, err := c.listPages(ctx, owner, kind, prev, maxPages)
		if err != nil || !changed {
			return l, err
		}
	}
	l, _, err := c.listPages(ctx, owner, kind, Pages{}, maxPages)
	return l, err
}

// listPages reads the pages, conditionally when etags are given, and
// reports whether any page changed. A conditional pass stops at the first
// changed page.
func (c *Client) listPages(ctx context.Context, owner string, kind OwnerKind, prev Pages, maxPages int) (Listing, bool, error) {
	l := Listing{}
	etags := prev.ETags
	conditional := len(etags) > 0
	for page := 1; ; page++ {
		if page > maxPages {
			l.Completeness = Partial
			l.Reason = fmt.Sprintf("more than %d repositories; read the rest when asked", maxPages*perPage)
			return l, true, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.restBase+listPath(owner, kind, page), nil)
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
			return l, false, nil
		}
		if conditional {
			_ = resp.Body.Close()
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
			l.Repos = append(l.Repos, r.listed(c.host))
		}
		if partial {
			l.Completeness, l.Reason = Partial, "this token is not authorized for the organization's SSO; only some repositories came back"
		}
		if len(repos) < perPage {
			if kind == OwnerUser && l.Completeness == Complete {
				l.Completeness, l.Reason = Partial, "another user's public repositories only"
			}
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
type Viewer struct {
	Login string
	Orgs  []string
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
	_, rate, err := c.graphql(ctx, "viewer", `query { viewer { login organizations(first: 100) { nodes { login } } } rateLimit { cost } }`, &d)
	if err != nil {
		return Viewer{}, rate, err
	}
	rate.Cost = d.RateLimit.Cost
	v := Viewer{Login: d.Viewer.Login}
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
