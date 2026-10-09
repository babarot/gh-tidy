package gitx

import (
	"strings"
	"testing"
)

func TestParseStatus(t *testing.T) {
	out := strings.Join([]string{
		"# branch.oid 1111111111111111111111111111111111111111",
		"# branch.head feature",
		"# branch.upstream origin/feature",
		"# branch.ab +2 -3",
		"# stash 4",
		"1 M. N... 100644 100644 100644 a a staged.txt",
		"1 .M N... 100644 100644 100644 a a unstaged.txt",
		"1 MM N... 100644 100644 100644 a a both.txt",
		"2 R. N... 100644 100644 100644 a a R100 new.txt", "old.txt",
		"u UU N... 100644 100644 100644 100644 a a a conflict.txt",
		"? untracked.txt",
		"? other.txt",
		"",
	}, "\x00")
	got := ParseStatus(out)
	want := Status{Head: "feature", Oid: strings.Repeat("1", 40), Upstream: "origin/feature", Ahead: 2, Behind: 3, HasAB: true,
		Staged: 3, Unstaged: 2, Untracked: 2, Conflicts: 1, Stash: 4}
	if got != want {
		t.Errorf("ParseStatus:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseStatusDetachedInitial(t *testing.T) {
	got := ParseStatus("# branch.oid (initial)\x00# branch.head (detached)\x00")
	if got.Head != "" || got.Oid != "" || got.HasAB {
		t.Errorf("ParseStatus = %+v, want an empty head and oid", got)
	}
}

func TestParseWorktrees(t *testing.T) {
	out := strings.Join([]string{
		"worktree /src/r", "HEAD aaa", "branch refs/heads/main", "",
		"worktree /wt/a", "HEAD bbb", "branch refs/heads/feature/x", "locked", "",
		"worktree /wt/gone", "HEAD ccc", "detached", "prunable gitdir file points to non-existent location", "",
	}, "\x00")
	got := ParseWorktrees(out)
	if len(got) != 3 {
		t.Fatalf("got %d worktrees, want 3: %+v", len(got), got)
	}
	if got[0].Path != "/src/r" || got[0].Branch != "main" {
		t.Errorf("main worktree = %+v", got[0])
	}
	if got[1].Branch != "feature/x" || !got[1].Locked {
		t.Errorf("second worktree = %+v", got[1])
	}
	if got[2].Branch != "" || !got[2].Prunable {
		t.Errorf("third worktree = %+v", got[2])
	}
}

func TestParseBranches(t *testing.T) {
	out := strings.Join([]string{
		"main\x00origin/main\x00\x001700000000",
		"ahead\x00origin/ahead\x00ahead 2, behind 1\x001700000001",
		"gone\x00origin/gone\x00gone\x001700000002",
		"local\x00\x00\x001700000003",
	}, "\n")
	got := ParseBranches(out)
	want := []Branch{
		{Name: "main", Upstream: "origin/main", Date: 1700000000},
		{Name: "ahead", Upstream: "origin/ahead", Ahead: 2, Behind: 1, Date: 1700000001},
		{Name: "gone", Upstream: "origin/gone", Gone: true, Date: 1700000002},
		{Name: "local", Date: 1700000003},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("branch %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct {
		url, host, slug string
		ok              bool
	}{
		{"git@github.com:babarot/gomi.git", "github.com", "babarot/gomi", true},
		{"ssh://git@github.com/k1low/mo", "github.com", "k1low/mo", true},
		{"ssh://git@ssh.github.com:443/babarot/claude-recall.git", "github.com", "babarot/claude-recall", true},
		{"https://github.com/babarot/gh-fleet", "github.com", "babarot/gh-fleet", true},
		{"https://token@github.com/babarot/gh-fleet.git/", "github.com", "babarot/gh-fleet", true},
		{"https://ghe.example.com/team/tool.git", "ghe.example.com", "team/tool", true},
		{"/local/path/repo", "", "", false},
		{"https://github.com/onlyowner", "", "", false},
	} {
		host, slug, ok := Slug(tc.url)
		if host != tc.host || slug != tc.slug || ok != tc.ok {
			t.Errorf("Slug(%q) = %q, %q, %v; want %q, %q, %v", tc.url, host, slug, ok, tc.host, tc.slug, tc.ok)
		}
	}
}
