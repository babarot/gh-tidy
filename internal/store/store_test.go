package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

func TestOwnerCacheRoundTrip(t *testing.T) {
	s := Store{CacheDir: t.TempDir(), StateDir: t.TempDir()}
	at := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	row := model.Row{Clone: "/src/github.com/me/app", Remote: "github.com/me/app"}
	row.Listed.Set(model.Remote{Key: "github.com/me/app", Name: "app"}, at)
	row.Heavy.Deny("alerts not readable", at)
	c := OwnerCache{Host: "github.com", Viewer: "me", Owner: "me", List: model.ListPartial, ListWhy: "why", ETags: []string{`W/"a"`}, Ended: true, Rows: []model.Row{row}}
	if err := s.SaveOwner(c); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LoadOwner("github.com", "ME", "me")
	if err != nil || !ok {
		t.Fatalf("load: %v, %v", ok, err)
	}
	if got.List != model.ListPartial || len(got.Rows) != 1 || !got.Rows[0].Listed.Has || got.Rows[0].Heavy.Last != model.Denied || got.ETags[0] != `W/"a"` {
		t.Errorf("loaded %+v", got)
	}
	path := s.ownerPath("github.com", "me", "me")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", fi.Mode().Perm(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestCacheOfAnotherAccountOrVersionIsNotUsed(t *testing.T) {
	s := Store{CacheDir: t.TempDir()}
	if err := s.SaveOwner(OwnerCache{Host: "github.com", Viewer: "me", Owner: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LoadOwner("github.com", "someone-else", "acme"); ok {
		t.Error("another account's cache was used")
	}
	if _, ok, _ := s.LoadOwner("ghe.example.com", "me", "acme"); ok {
		t.Error("another host's cache was used")
	}
	path := s.ownerPath("github.com", "me", "acme")
	if err := os.WriteFile(path, []byte(`{"Version":99,"Host":"github.com","Viewer":"me","Owner":"acme"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadOwner("github.com", "me", "acme"); ok || err != nil {
		t.Errorf("a cache of another version: %v, %v", ok, err)
	}
	if err := os.WriteFile(path, []byte(`{"Vers`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.LoadOwner("github.com", "me", "acme"); ok || err != nil {
		t.Errorf("a cache cut short: %v, %v", ok, err)
	}
}

func TestState(t *testing.T) {
	s := Store{StateDir: filepath.Join(t.TempDir(), "state")}
	st, err := s.LoadState()
	if err != nil || len(st.LastOwner) != 0 {
		t.Fatalf("empty state: %+v, %v", st, err)
	}
	st.LastOwner[AccountKey("github.com", "Me")] = "acme"
	st.Lens, st.PreviewHeight = "github", 12
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadState()
	if err != nil || got.LastOwner["github.com/me"] != "acme" || got.Lens != "github" || got.PreviewHeight != 12 {
		t.Errorf("state = %+v, %v", got, err)
	}
}

func TestSafeNames(t *testing.T) {
	for in, want := range map[string]string{"../etc": "__etc", "a/b": "a_b", "GitHub.com": "github.com"} {
		if got := safe(in); got != want {
			t.Errorf("safe(%q) = %q, want %q", in, got, want)
		}
	}
}
