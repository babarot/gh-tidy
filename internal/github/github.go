// Package github reads repositories from GitHub through gh's own
// authentication. Asking for each repository's default branch commit while
// listing an owner's repositories is slow (half a minute for 400), so the
// two are separate queries: the clones' default branches by name, batched
// with aliases, and the owner's list without them.
package github

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Repo is a repository on GitHub.
type Repo struct {
	Name          string
	NameWithOwner string
	Archived      bool
	Fork          bool
	Visibility    string // PUBLIC, PRIVATE or INTERNAL
	PushedAt      time.Time
	Stars         int
	Default       string // the default branch; empty for an empty repository
	DefaultOid    string // the commit it is at
}

// ErrNoOwner is returned when no user or organization has the login.
var ErrNoOwner = errors.New("no such user or organization on GitHub")

const listQuery = `query($login: String!, $after: String) {
  repositoryOwner(login: $login) {
    repositories(first: 100, after: $after, ownerAffiliations: OWNER) {
      pageInfo { hasNextPage endCursor }
      nodes { name nameWithOwner isArchived isFork visibility pushedAt stargazerCount }
    }
  }
}`

// List returns every repository owner owns on host, without their default
// branches.
func List(ctx context.Context, host, owner string) ([]Repo, error) {
	client, err := api.NewGraphQLClient(api.ClientOptions{Host: host})
	if err != nil {
		return nil, err
	}
	var out []Repo
	vars := map[string]any{"login": owner, "after": nil}
	for {
		var resp struct {
			RepositoryOwner *struct {
				Repositories struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []struct {
						Name           string
						NameWithOwner  string
						IsArchived     bool
						IsFork         bool
						Visibility     string
						PushedAt       time.Time
						StargazerCount int
					}
				}
			}
		}
		if err := client.DoWithContext(ctx, listQuery, vars, &resp); err != nil {
			return out, err
		}
		if resp.RepositoryOwner == nil {
			return nil, ErrNoOwner
		}
		rs := resp.RepositoryOwner.Repositories
		for _, n := range rs.Nodes {
			out = append(out, Repo{Name: n.Name, NameWithOwner: n.NameWithOwner, Archived: n.IsArchived, Fork: n.IsFork,
				Visibility: n.Visibility, PushedAt: n.PushedAt, Stars: n.StargazerCount})
		}
		if !rs.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = rs.PageInfo.EndCursor
	}
}

// batch is how many repositories one query asks for.
const batch = 50

// Lookup returns the repositories named by slugs (owner/name), with their
// default branches, by lower-case owner/name. One that does not exist, or
// is not visible, is left out.
func Lookup(ctx context.Context, host string, slugs []string) (map[string]Repo, error) {
	client, err := api.NewGraphQLClient(api.ClientOptions{Host: host})
	if err != nil {
		return nil, err
	}
	out := map[string]Repo{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error
	for start := 0; start < len(slugs); start += batch {
		part := slugs[start:min(len(slugs), start+batch)]
		wg.Go(func() {
			got, err := lookup(ctx, client, part)
			mu.Lock()
			defer mu.Unlock()
			maps.Copy(out, got)
			if err != nil {
				errs = append(errs, err)
			}
		})
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

type node struct {
	Name             string
	NameWithOwner    string
	IsArchived       bool
	IsFork           bool
	Visibility       string
	PushedAt         time.Time
	StargazerCount   int
	DefaultBranchRef *struct {
		Name   string
		Target struct{ Oid string }
	}
}

func lookup(ctx context.Context, client *api.GraphQLClient, slugs []string) (map[string]Repo, error) {
	var q strings.Builder
	q.WriteString("query {")
	for i, s := range slugs {
		owner, name, _ := strings.Cut(s, "/")
		fmt.Fprintf(&q, " r%d: repository(owner: %q, name: %q) { name nameWithOwner isArchived isFork visibility pushedAt stargazerCount defaultBranchRef { name target { oid } } }", i, owner, name)
	}
	q.WriteString(" }")
	resp := map[string]*node{}
	err := client.DoWithContext(ctx, q.String(), nil, &resp)
	var gqlErr *api.GraphQLError
	if errors.As(err, &gqlErr) && onlyNotFound(gqlErr) {
		err = nil // a repository deleted or renamed away: the others came back
	}
	out := map[string]Repo{}
	for _, n := range resp {
		if n == nil {
			continue
		}
		r := Repo{Name: n.Name, NameWithOwner: n.NameWithOwner, Archived: n.IsArchived, Fork: n.IsFork,
			Visibility: n.Visibility, PushedAt: n.PushedAt, Stars: n.StargazerCount}
		if n.DefaultBranchRef != nil {
			r.Default, r.DefaultOid = n.DefaultBranchRef.Name, n.DefaultBranchRef.Target.Oid
		}
		out[strings.ToLower(n.NameWithOwner)] = r
	}
	// A renamed repository answers under its new name; keep it under the
	// name the clone asked for too.
	for i, s := range slugs {
		if n := resp[fmt.Sprintf("r%d", i)]; n != nil {
			out[strings.ToLower(s)] = out[strings.ToLower(n.NameWithOwner)]
		}
	}
	return out, err
}

func onlyNotFound(e *api.GraphQLError) bool {
	for _, x := range e.Errors {
		if x.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}
