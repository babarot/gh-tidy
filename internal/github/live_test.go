package github

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestLive asks GitHub for real; set TIDY_LIVE=1 to run it.
func TestLive(t *testing.T) {
	if os.Getenv("TIDY_LIVE") == "" {
		t.Skip("set TIDY_LIVE=1 to ask GitHub")
	}
	ctx := context.Background()
	start := time.Now()
	got, err := Lookup(ctx, "github.com", []string{"cli/cli", "babarot/does-not-exist-tidy", "babarot/za-prompt"})
	t.Logf("Lookup: %d, %v, %s", len(got), err, time.Since(start))
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := got["cli/cli"]; !ok || r.Default != "trunk" || r.DefaultOid == "" {
		t.Errorf("cli/cli = %+v", r)
	}
	if _, ok := got["babarot/does-not-exist-tidy"]; ok {
		t.Errorf("a missing repository came back")
	}
	if _, err := List(ctx, "github.com", "no-such-owner-for-tidy-test"); !errors.Is(err, ErrNoOwner) {
		t.Errorf("List of a missing owner = %v, want ErrNoOwner", err)
	}
}
