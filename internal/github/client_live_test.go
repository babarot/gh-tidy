package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("TIDY_LIVE") == "" {
		t.Skip("set TIDY_LIVE=1 to ask GitHub")
	}
	c, err := NewClient(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestLiveListConditional reads the viewer's list twice: the second time
// must answer 304 on every page and cost nothing.
func TestLiveListConditional(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	v, _, err := c.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	l, err := c.List(ctx, v.Login, OwnerViewer, Pages{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first read: %d repositories, %d pages, %s, cost %d, %d left", len(l.Repos), len(l.Pages.ETags), time.Since(start), l.Rate.Cost, l.Rate.Remaining)
	start = time.Now()
	again, err := c.List(ctx, v.Login, OwnerViewer, l.Pages, 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("again: unchanged %v, %s, cost %d, %d left", again.Unchanged, time.Since(start), again.Rate.Cost, again.Rate.Remaining)
	if !again.Unchanged || again.Rate.Cost != 0 {
		t.Errorf("asking again: unchanged %v, cost %d", again.Unchanged, again.Rate.Cost)
	}
	if again.Rate.Known() && l.Rate.Known() && again.Rate.Remaining < l.Rate.Remaining-1 {
		t.Errorf("304s used points: %d left before, %d after", l.Rate.Remaining, again.Rate.Remaining)
	}
}

// TestLiveHeavy reads every field for batches of ten of the viewer's own
// repositories (not forks, not archived), and reports how long each batch
// took: GitHub cuts off a query past ten seconds.
func TestLiveHeavy(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	v, _, err := c.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.List(ctx, v.Login, OwnerViewer, Pages{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, r := range l.Repos {
		if !r.Fork && !r.Archived {
			slugs = append(slugs, r.NameWithOwner)
		}
	}
	slugs = append(slugs[:min(len(slugs), 30)], "babarot/does-not-exist-tidy")
	off, counted := 0, 0
	for i := 0; i < len(slugs); i += 10 {
		part := slugs[i:min(len(slugs), i+10)]
		start := time.Now()
		got, rate, err := c.Heavy(ctx, part, Want{Default: true, CI: true, Counts: true, Alerts: true})
		if err != nil {
			t.Fatalf("batch %d: %v", i/10, err)
		}
		t.Logf("batch of %d: %s, cost %d, %d left", len(part), time.Since(start), rate.Cost, rate.Remaining)
		for _, h := range got {
			switch {
			case !h.Found && h.Asked != "github.com/babarot/does-not-exist-tidy":
				t.Errorf("%s not found", h.Asked)
			case h.AlertsOff:
				off++
			case h.Found && !h.AlertsDenied:
				counted++
			}
		}
	}
	t.Logf("Dependabot alerts: %d counted, %d off", counted, off)

	// A public repository of another owner: GitHub counts its alerts as 0,
	// with no error, to a token that may not read them.
	got, _, err := c.Heavy(ctx, []string{"cli/cli"}, Want{Alerts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Found || !got[0].AlertsDenied {
		t.Errorf("another owner's alerts = %+v, want not readable", got[0])
	}
	if _, err := c.List(ctx, "no-such-owner-for-tidy-test", OwnerUser, Pages{}, 1); !errors.Is(err, ErrNoOwner) {
		t.Errorf("a missing owner: %v", err)
	}
}

// TestLiveOrg answers Q8 for an organization: set TIDY_LIVE=1 and
// TIDY_LIVE_ORG=<login>. It prints only counts and states, never the names
// of repositories, so that its output can be shared.
func TestLiveOrg(t *testing.T) {
	c := liveClient(t)
	org := os.Getenv("TIDY_LIVE_ORG")
	if org == "" {
		t.Skip("set TIDY_LIVE_ORG to an organization")
	}
	ctx := context.Background()
	v, _, err := c.Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kind := v.KindOf(org)
	t.Logf("token's organizations: %d (some left out: %v); the organization is a member organization: %v", len(v.Orgs), v.OrgsIncomplete, kind == OwnerOrg)
	start := time.Now()
	l, err := c.List(ctx, org, kind, Pages{}, 20)
	if err != nil {
		t.Fatalf("list failed: %s", errorShape(err))
	}
	private := 0
	for _, r := range l.Repos {
		if r.Private {
			private++
		}
	}
	t.Logf("list: %d repositories (%d private), %d pages, completeness %d (%s), %s, cost %d", len(l.Repos), private, len(l.Pages.ETags), l.Completeness, l.Reason, time.Since(start), l.Rate.Cost)
	var slugs []string
	for _, r := range l.Repos {
		if !r.Fork && !r.Archived {
			slugs = append(slugs, r.NameWithOwner)
		}
	}
	slugs = slugs[:min(len(slugs), 10)]
	if len(slugs) == 0 {
		return
	}
	start = time.Now()
	got, rate, err := c.Heavy(ctx, slugs, Want{Default: true, CI: true, Counts: true, Alerts: true})
	if err != nil {
		t.Fatalf("heavy failed as a whole: %s", errorShape(err))
	}
	found, off, denied, counted, errs := 0, 0, 0, 0, 0
	for _, h := range got {
		if h.Found {
			found++
		}
		switch {
		case h.Err != "":
			errs++
		case h.AlertsOff:
			off++
		case h.AlertsDenied:
			denied++
		case h.Found:
			counted++
		}
	}
	t.Logf("heavy, %d repositories: %s, cost %d; found %d; Dependabot counted %d, off %d, not readable %d; other errors %d", len(slugs), time.Since(start), rate.Cost, found, counted, off, denied, errs)
}

// errorShape says what kind of error err is without its message, which
// may name a repository.
func errorShape(err error) string {
	var rl *RateLimitError
	var se *StatusError
	switch {
	case errors.As(err, &rl):
		return fmt.Sprintf("rate limit (primary %v)", rl.Primary)
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.As(err, &se):
		return fmt.Sprintf("status %d, SSO header %v", se.Status, se.SSO != "")
	case errors.Is(err, ErrNoOwner):
		return "no such owner"
	}
	return fmt.Sprintf("%T", err)
}
