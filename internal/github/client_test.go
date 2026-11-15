package github

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

func TestListAsksAgainForFree(t *testing.T) {
	f := newFake()
	for i := range 150 {
		f.addRepos("me", fmt.Sprintf("repo-%03d", i))
	}
	c := f.client(t)
	ctx := context.Background()
	l, err := c.List(ctx, "me", OwnerViewer, Pages{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Repos) != 150 || len(l.Pages.ETags) != 2 || l.Completeness != Complete || l.Rate.Cost != 2 {
		t.Fatalf("first read: %d repos, %d pages, %v, cost %d", len(l.Repos), len(l.Pages.ETags), l.Completeness, l.Rate.Cost)
	}
	if l.Repos[0].Key != model.RemoteKey("github.com/me/repo-000") {
		t.Errorf("key = %q", l.Repos[0].Key)
	}

	before := f.remaining
	again, err := c.List(ctx, "me", OwnerViewer, l.Pages, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged || len(again.Repos) != 0 || f.remaining != before || again.Rate.Cost != 0 {
		t.Errorf("unchanged list: unchanged %v, %d repos, used %d points", again.Unchanged, len(again.Repos), before-f.remaining)
	}

	// One repository changes on the second page: every page is read again,
	// so the list is not put together from pages of two moments.
	f.repos["me"][120].PushedAt = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	changed, err := c.List(ctx, "me", OwnerViewer, l.Pages, 10)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Unchanged || len(changed.Repos) != 150 || !changed.Repos[120].PushedAt.Equal(f.repos["me"][120].PushedAt) {
		t.Errorf("changed list: unchanged %v, %d repos", changed.Unchanged, len(changed.Repos))
	}
}

func TestListSeesARepositoryAddedAfterAFullLastPage(t *testing.T) {
	f := newFake()
	for i := range 100 {
		f.addRepos("me", fmt.Sprintf("a-%03d", i))
	}
	c := f.client(t)
	ctx := context.Background()
	l, err := c.List(ctx, "me", OwnerViewer, Pages{}, 10)
	if err != nil || len(l.Pages.ETags) != 2 {
		t.Fatalf("first read: %v, %d pages (a full page, then an empty one)", err, len(l.Pages.ETags))
	}
	if again, _ := c.List(ctx, "me", OwnerViewer, l.Pages, 10); !again.Unchanged {
		t.Fatal("nothing changed, but the list says it did")
	}
	f.addRepos("me", "z-last") // sorts after every page read before
	again, err := c.List(ctx, "me", OwnerViewer, l.Pages, 10)
	if err != nil || again.Unchanged || len(again.Repos) != 101 {
		t.Errorf("added after a full page: unchanged %v, %d repos, %v", again.Unchanged, len(again.Repos), err)
	}
}

func TestListCompleteness(t *testing.T) {
	f := newFake()
	f.addRepos("acme", "api", "web")
	f.addRepos("kumo", "console")
	f.sso["kumo"] = true
	f.addRepos("half", "public-one")
	f.partial["half"] = true
	f.addRepos("someone", "tool")
	for i := range 250 {
		f.addRepos("big", fmt.Sprintf("r%03d", i))
	}
	c := f.client(t)
	ctx := context.Background()
	for _, tc := range []struct {
		owner  string
		kind   OwnerKind
		want   Completeness
		reason string
	}{
		{"acme", OwnerOrg, Complete, ""},
		{"kumo", OwnerOrg, Unreadable, "SSO"},
		{"half", OwnerOrg, Partial, "SSO"},
		{"someone", OwnerUser, Partial, "public"},
		{"big", OwnerOrg, Partial, "more than 200"},
	} {
		l, err := c.List(ctx, tc.owner, tc.kind, Pages{}, 2)
		if err != nil {
			t.Fatalf("%s: %v", tc.owner, err)
		}
		if l.Completeness != tc.want || !strings.Contains(l.Reason, tc.reason) {
			t.Errorf("%s: %v (%q), want %v (%q)", tc.owner, l.Completeness, l.Reason, tc.want, tc.reason)
		}
	}
	if _, err := c.List(ctx, "nobody", OwnerUser, Pages{}, 2); !errors.Is(err, ErrNoOwner) {
		t.Errorf("an owner that does not exist: %v", err)
	}
}

func TestHeavy(t *testing.T) {
	f := newFake()
	pushed := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	f.nodes["me/app"] = fakeNode{pushed: pushed, def: "main", oid: "abc", ci: "FAILURE", issues: 3, prs: 1, alerts: 2}
	f.nodes["me/new-name"] = fakeNode{pushed: pushed, def: "trunk", oid: "def"}
	f.renamed["me/old-name"] = "me/new-name"
	f.nodes["me/no-alerts"] = fakeNode{pushed: pushed, def: "main", oid: "1", alertsOff: true}
	f.nodes["org/secret"] = fakeNode{pushed: pushed, def: "main", oid: "2"}
	f.denied["org/secret"] = true
	c := f.client(t)
	got, rate, err := c.Heavy(context.Background(), []string{"me/app", "me/old-name", "me/gone", "me/no-alerts", "org/secret"}, Want{Default: true, CI: true, Counts: true, Alerts: true})
	if err != nil {
		t.Fatal(err)
	}
	if rate.Cost != 1 || rate.Remaining == 0 {
		t.Errorf("rate = %+v", rate)
	}
	app, renamed, gone, off, secret := got[0], got[1], got[2], got[3], got[4]
	if !app.Found || app.Default != "main" || app.DefaultOid != "abc" || app.CI != "FAILURE" || app.Issues != 3 || app.PRs != 1 || app.Alerts != 2 || !app.PushedAt.Equal(pushed) {
		t.Errorf("app = %+v", app)
	}
	if !renamed.Found || renamed.Asked != "github.com/me/old-name" || renamed.Key != "github.com/me/new-name" || renamed.Default != "trunk" {
		t.Errorf("renamed = %+v", renamed)
	}
	if gone.Found || gone.Err != "" {
		t.Errorf("gone = %+v", gone)
	}
	if !off.AlertsOff || off.Alerts != 0 || off.AlertsDenied {
		t.Errorf("alerts off = %+v", off)
	}
	if !secret.AlertsDenied || secret.AlertsOff || secret.Err != "" {
		t.Errorf("alerts denied = %+v", secret)
	}
}

func TestHeavyAsksOnlyForWhatIsWanted(t *testing.T) {
	f := newFake()
	f.nodes["me/app"] = fakeNode{def: "main", oid: "abc"}
	c := f.client(t)
	if _, _, err := c.Heavy(context.Background(), []string{"me/app"}, Want{Default: true}); err != nil {
		t.Fatal(err)
	}
	q := f.queries[len(f.queries)-1]
	for _, field := range []string{"statusCheckRollup", "issues(", "vulnerabilityAlerts"} {
		if strings.Contains(q, field) {
			t.Errorf("asked for %s without wanting it: %s", field, q)
		}
	}
	if !strings.Contains(q, "defaultBranchRef") {
		t.Errorf("did not ask for the default branch: %s", q)
	}
}

func TestRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("timeout", func(t *testing.T) {
		f := newFake()
		f.timeouts = 1
		if _, _, err := f.client(t).Heavy(ctx, []string{"me/app"}, Want{Default: true}); !errors.Is(err, ErrTimeout) {
			t.Errorf("err = %v, want ErrTimeout", err)
		}
	})
	t.Run("secondary", func(t *testing.T) {
		f := newFake()
		f.addRepos("me", "app")
		f.secondary = 1
		_, err := f.client(t).List(ctx, "me", OwnerViewer, Pages{}, 2)
		var rl *RateLimitError
		if !errors.As(err, &rl) || rl.Primary || rl.Wait() != 30*time.Second {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("primary, REST", func(t *testing.T) {
		f := newFake()
		f.addRepos("me", "app")
		f.primaryOut = true
		_, err := f.client(t).List(ctx, "me", OwnerViewer, Pages{}, 2)
		var rl *RateLimitError
		if !errors.As(err, &rl) || !rl.Primary || rl.Reset.IsZero() {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("primary, GraphQL", func(t *testing.T) {
		f := newFake()
		f.primaryOut = true
		_, _, err := f.client(t).Heavy(ctx, []string{"me/app"}, Want{Default: true})
		var rl *RateLimitError
		if !errors.As(err, &rl) || !rl.Primary {
			t.Errorf("err = %v", err)
		}
	})
}

func TestViewer(t *testing.T) {
	f := newFake()
	f.orgs = []string{"acme", "Kumo"}
	v, _, err := f.client(t).Viewer(context.Background())
	if err != nil || v.Login != "me" {
		t.Fatalf("viewer = %+v, %v", v, err)
	}
	for owner, want := range map[string]OwnerKind{"me": OwnerViewer, "ME": OwnerViewer, "kumo": OwnerOrg, "someone": OwnerUser} {
		if got := v.KindOf(owner); got != want {
			t.Errorf("KindOf(%s) = %v, want %v", owner, got, want)
		}
	}
}

func TestUnchangedCarriesCompletenessForward(t *testing.T) {
	f := newFake()
	f.addRepos("someone", "tool")
	c := f.client(t)
	ctx := context.Background()
	l, err := c.List(ctx, "someone", OwnerUser, Pages{}, 5)
	if err != nil || l.Completeness != Partial {
		t.Fatalf("first read: %v, %v", l.Completeness, err)
	}
	again, err := c.List(ctx, "someone", OwnerUser, l.Pages, 5)
	if err != nil || !again.Unchanged || again.Completeness != Partial || again.Reason != l.Reason {
		t.Errorf("unchanged: %v %v (%q), %v", again.Unchanged, again.Completeness, again.Reason, err)
	}
}

func TestACutOffListIsReadWholeWhenAskedForMore(t *testing.T) {
	f := newFake()
	for i := range 250 {
		f.addRepos("big", fmt.Sprintf("r%03d", i))
	}
	c := f.client(t)
	ctx := context.Background()
	l, err := c.List(ctx, "big", OwnerOrg, Pages{}, 2)
	if err != nil || l.Completeness != Partial || len(l.Repos) != 200 {
		t.Fatalf("cut off: %v, %d repos, %v", l.Completeness, len(l.Repos), err)
	}
	if again, _ := c.List(ctx, "big", OwnerOrg, l.Pages, 2); !again.Unchanged || again.Completeness != Partial {
		t.Errorf("asked again within the limit: unchanged %v, %v", again.Unchanged, again.Completeness)
	}
	all, err := c.List(ctx, "big", OwnerOrg, l.Pages, 10)
	if err != nil || all.Unchanged || all.Completeness != Complete || len(all.Repos) != 250 {
		t.Errorf("asked for the rest: unchanged %v, %v, %d repos, %v", all.Unchanged, all.Completeness, len(all.Repos), err)
	}
}

func TestCostCountsThePageThatShowedAChange(t *testing.T) {
	f := newFake()
	for i := range 150 {
		f.addRepos("me", fmt.Sprintf("r%03d", i))
	}
	c := f.client(t)
	ctx := context.Background()
	l, _ := c.List(ctx, "me", OwnerViewer, Pages{}, 5)
	f.repos["me"][0].Description = "changed"
	before := f.remaining
	again, err := c.List(ctx, "me", OwnerViewer, l.Pages, 5)
	if err != nil || again.Rate.Cost != before-f.remaining {
		t.Errorf("cost %d, points used %d, %v", again.Rate.Cost, before-f.remaining, err)
	}
}

func TestAListReadWithAnErrorIsNotComplete(t *testing.T) {
	f := newFake()
	l, err := f.client(t).List(context.Background(), "nobody", OwnerOrg, Pages{}, 2)
	if err == nil || l.Completeness == Complete {
		t.Errorf("list of a missing owner: %v, %v", l.Completeness, err)
	}
}

func TestHeavyNeverReadsAFailureAsMissing(t *testing.T) {
	ctx := context.Background()
	t.Run("the whole query failed", func(t *testing.T) {
		f := newFake()
		f.nodes["me/app"] = fakeNode{def: "main"}
		f.failWhole = true
		got, _, err := f.client(t).Heavy(ctx, []string{"me/app"}, Want{Default: true})
		if err == nil || got != nil {
			t.Errorf("a failed query: %+v, %v", got, err)
		}
	})
	t.Run("no answer for one", func(t *testing.T) {
		f := newFake()
		f.nodes["me/a"] = fakeNode{def: "main"}
		f.nodes["me/b"] = fakeNode{def: "main"}
		f.dropAlias = map[string]bool{"r1": true}
		got, _, err := f.client(t).Heavy(ctx, []string{"me/a", "me/b"}, Want{Default: true})
		if err != nil || !got[0].Found || got[1].Found || got[1].Err == "" {
			t.Errorf("no answer for b: %+v, %v", got, err)
		}
	})
}

func TestViewerThatCannotBeReadIsAnError(t *testing.T) {
	f := newFake()
	f.viewerFails = true
	if _, _, err := f.client(t).Viewer(context.Background()); err == nil {
		t.Error("a viewer GitHub would not give came back without an error")
	}
}

func TestViewerWithOrganizationsLeftOut(t *testing.T) {
	f := newFake()
	f.orgsFail = true
	v, _, err := f.client(t).Viewer(context.Background())
	if err != nil || v.Login != "me" || !v.OrgsIncomplete || v.KindOf("me") != OwnerViewer {
		t.Errorf("viewer = %+v, %v", v, err)
	}
}
