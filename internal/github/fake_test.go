package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a made-up GitHub behind the client's Transport: no network.
type fake struct {
	mu      sync.Mutex
	viewer  string
	orgs    []string
	repos   map[string][]restRepo // by owner, in name order
	sso     map[string]bool       // orgs the token is not authorized for
	partial map[string]bool       // orgs that answer with some repositories only
	nodes   map[string]fakeNode   // GraphQL repositories by lower-case owner/name
	renamed map[string]string     // old lower-case owner/name to the new name
	denied  map[string]bool       // repositories whose alerts the token may not read

	// What to do to the next requests, and what was asked.
	timeouts, secondary int
	primaryOut          bool
	failWhole           bool            // GraphQL answers data null and an error with no path
	dropAlias           map[string]bool // aliases left out of the data with no error
	viewerFails         bool
	orgsFail            bool // the login comes back, the organizations with an error
	queries             []string
	remaining           int
	requests            int
}

type fakeNode struct {
	pushed    time.Time
	stars     int
	def, oid  string
	ci        string
	issues    int
	prs       int
	alerts    int
	alertsOff bool
	// perm is the token's role; empty is ADMIN. A role that cannot read
	// the alerts gets a count of 0 and turned on, as GitHub answers.
	perm string
	// alertList and checks are what the detail of the repository holds:
	// alerts as GitHub gives them, and check nodes of the commit.
	alertList []map[string]any
	checks    []map[string]any
}

var detailRe = regexp.MustCompile(`^query \{ repository\(owner: "([^"]+)", name: "([^"]+)"\)`)

// detail answers the query of one repository's detail.
func (f *fake) detail(w http.ResponseWriter, query, slug string) {
	n, ok := f.nodes[slug]
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": nil},
			"errors": []map[string]any{{"type": "NOT_FOUND", "path": []string{"repository"}, "message": "Could not resolve to a Repository"}}})
		return
	}
	repo := map[string]any{}
	var errs []map[string]any
	if strings.Contains(query, "vulnerabilityAlerts") {
		if f.denied[slug] {
			repo["vulnerabilityAlerts"] = nil
			errs = append(errs, map[string]any{"type": "FORBIDDEN", "path": []string{"repository", "vulnerabilityAlerts"}, "message": "no access"})
		} else {
			repo["vulnerabilityAlerts"] = map[string]any{"totalCount": len(n.alertList), "nodes": n.alertList}
		}
	}
	if strings.Contains(query, "object(oid:") {
		failed, running := 0, 0
		for _, c := range n.checks {
			switch {
			case c["conclusion"] == "FAILURE" || c["state"] == "FAILURE":
				failed++
			case c["status"] == "IN_PROGRESS" || c["state"] == "PENDING":
				running++
			}
		}
		repo["object"] = map[string]any{"statusCheckRollup": map[string]any{"state": n.ci, "contexts": map[string]any{
			"checkRunCountsByState":      []map[string]any{{"state": "FAILURE", "count": failed}, {"state": "IN_PROGRESS", "count": running}},
			"statusContextCountsByState": []map[string]any{}, "nodes": n.checks}}}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": repo, "rateLimit": map[string]int{"cost": 1}}, "errors": errs})
}

func newFake() *fake {
	return &fake{viewer: "me", repos: map[string][]restRepo{}, sso: map[string]bool{}, partial: map[string]bool{},
		nodes: map[string]fakeNode{}, renamed: map[string]string{}, denied: map[string]bool{}, remaining: 5000}
}

func (f *fake) client(t *testing.T) *Client {
	t.Helper()
	c, err := NewClient(Options{Transport: roundTrip{f}, AuthToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fake) addRepos(owner string, names ...string) {
	for _, n := range names {
		f.repos[owner] = append(f.repos[owner], restRepo{Name: n, FullName: owner + "/" + n, DefaultBranch: "main", PushedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			Stars: len(f.repos[owner])})
	}
	slices.SortFunc(f.repos[owner], func(a, b restRepo) int { return strings.Compare(a.Name, b.Name) })
}

type roundTrip struct{ f *fake }

func (r roundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.f.ServeHTTP(rec, req)
	return rec.Result(), nil
}

func (f *fake) rateHeaders(w http.ResponseWriter, resource string) {
	h := w.Header()
	h.Set("X-Ratelimit-Limit", "5000")
	h.Set("X-Ratelimit-Remaining", strconv.Itoa(f.remaining))
	h.Set("X-Ratelimit-Used", strconv.Itoa(5000-f.remaining))
	h.Set("X-Ratelimit-Reset", strconv.FormatInt(time.Date(2026, 11, 15, 13, 0, 0, 0, time.UTC).Unix(), 10))
	h.Set("X-Ratelimit-Resource", resource)
	h.Set("X-Github-Request-Id", fmt.Sprintf("REQ%d", f.requests))
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch {
	case f.timeouts > 0:
		f.timeouts--
		w.WriteHeader(http.StatusBadGateway)
		return
	case f.secondary > 0:
		f.secondary--
		f.rateHeaders(w, "core")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
		return
	case f.primaryOut && r.Method == http.MethodGet:
		f.remaining = 0
		f.rateHeaders(w, "core")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
		return
	}
	if r.URL.Path == "/graphql" {
		f.graphql(w, r)
		return
	}
	var owner string
	switch p := r.URL.Path; {
	case p == "/user/repos":
		owner = f.viewer
	case strings.HasPrefix(p, "/orgs/"):
		owner = strings.TrimSuffix(strings.TrimPrefix(p, "/orgs/"), "/repos")
		if f.sso[owner] {
			f.rateHeaders(w, "core")
			w.Header().Set("X-Github-Sso", "required; url=https://github.com/orgs/"+owner+"/sso")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource protected by organization SAML enforcement."}`)
			return
		}
	case strings.HasPrefix(p, "/users/"):
		owner = strings.TrimSuffix(strings.TrimPrefix(p, "/users/"), "/repos")
	}
	all, ok := f.repos[owner]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	from, to := min((page-1)*per, len(all)), min(page*per, len(all))
	body, _ := json.Marshal(all[from:to])
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	f.rateHeaders(w, "core")
	w.Header().Set("Etag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified) // free: the remaining count is not touched
		return
	}
	f.remaining--
	f.rateHeaders(w, "core")
	if f.partial[owner] {
		w.Header().Set("X-Github-Sso", "partial-results; organizations=1")
	}
	_, _ = w.Write(body)
}

var aliasRe = regexp.MustCompile(`(r\d+): repository\(owner: "([^"]+)", name: "([^"]+)"\)`)

func (f *fake) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct{ Query string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.queries = append(f.queries, req.Query)
	f.remaining--
	f.rateHeaders(w, "graphql")
	if f.primaryOut {
		_, _ = io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
		return
	}
	if f.failWhole {
		_, _ = io.WriteString(w, `{"data":null,"errors":[{"message":"Something went wrong while executing your query."}]}`)
		return
	}
	if m := detailRe.FindStringSubmatch(req.Query); m != nil {
		f.detail(w, req.Query, strings.ToLower(m[1]+"/"+m[2]))
		return
	}
	if strings.Contains(req.Query, "viewer {") && f.orgsFail {
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"me","organizations":null}},"errors":[{"type":"FORBIDDEN","path":["viewer","organizations"],"message":"SAML enforcement"}]}`)
		return
	}
	if strings.Contains(req.Query, "viewer {") && f.viewerFails {
		_, _ = io.WriteString(w, `{"data":{"viewer":null},"errors":[{"type":"INSUFFICIENT_SCOPES","path":["viewer"],"message":"needs read:org"}]}`)
		return
	}
	if strings.Contains(req.Query, "viewer {") {
		var orgs []map[string]string
		for _, o := range f.orgs {
			orgs = append(orgs, map[string]string{"login": o})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"viewer": map[string]any{"login": f.viewer, "organizations": map[string]any{"nodes": orgs}}, "rateLimit": map[string]int{"cost": 1}}})
		return
	}
	data := map[string]any{"rateLimit": map[string]int{"cost": 1}}
	var errs []map[string]any
	ms := aliasRe.FindAllStringSubmatchIndex(req.Query, -1)
	for i, m := range ms {
		end := len(req.Query)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		alias, slug, fields := req.Query[m[2]:m[3]], strings.ToLower(req.Query[m[4]:m[5]]+"/"+req.Query[m[6]:m[7]]), req.Query[m[1]:end]
		if f.dropAlias[alias] {
			continue
		}
		if to, ok := f.renamed[slug]; ok {
			slug = to
		}
		n, ok := f.nodes[slug]
		if !ok {
			data[alias] = nil
			errs = append(errs, map[string]any{"type": "NOT_FOUND", "path": []string{alias}, "message": "Could not resolve to a Repository"})
			continue
		}
		v := map[string]any{"nameWithOwner": slug, "pushedAt": n.pushed}
		if strings.Contains(fields, "stargazerCount") {
			v["stargazerCount"] = n.stars
		}
		if strings.Contains(fields, "defaultBranchRef") {
			target := map[string]any{"oid": n.oid}
			if strings.Contains(fields, "statusCheckRollup") {
				if n.ci != "" {
					target["statusCheckRollup"] = map[string]string{"state": n.ci}
				} else {
					target["statusCheckRollup"] = nil
				}
			}
			v["defaultBranchRef"] = map[string]any{"name": n.def, "target": target}
		}
		if strings.Contains(fields, "issues(") {
			v["issues"] = map[string]int{"totalCount": n.issues}
			v["pullRequests"] = map[string]int{"totalCount": n.prs}
		}
		if strings.Contains(fields, "viewerPermission") {
			perm := n.perm
			if perm == "" {
				perm = "ADMIN"
			}
			v["viewerPermission"] = perm
		}
		if strings.Contains(fields, "vulnerabilityAlerts") {
			v["hasVulnerabilityAlertsEnabled"] = !n.alertsOff
			if n.perm == "READ" || n.perm == "TRIAGE" {
				v["hasVulnerabilityAlertsEnabled"] = true
				v["vulnerabilityAlerts"] = map[string]int{"totalCount": n.alerts}
			} else if f.denied[slug] {
				v["vulnerabilityAlerts"] = nil
				errs = append(errs, map[string]any{"type": "FORBIDDEN", "path": []string{alias, "vulnerabilityAlerts"}, "message": "Resource not accessible by integration"})
			} else {
				v["vulnerabilityAlerts"] = map[string]int{"totalCount": n.alerts}
			}
		}
		data[alias] = v
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "errors": errs})
}
