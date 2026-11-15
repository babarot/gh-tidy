package model

import "testing"

func TestKeys(t *testing.T) {
	if got := NewRemoteKey("github.com", "Babarot/GH-Tidy"); got != "github.com/babarot/gh-tidy" {
		t.Errorf("remote key = %q", got)
	}
	if got := NewRemoteKey("github.com", ""); got != "" {
		t.Errorf("a clone without a slug has remote key %q", got)
	}
	if NewCloneKey("/src/a/") != NewCloneKey("/src/a") {
		t.Error("clone keys of one path differ")
	}
}

func TestOnlyGitHubOrOriginHEADIsTrusted(t *testing.T) {
	for s, want := range map[DefaultSource]bool{DefaultFromGitHub: true, DefaultFromOriginHEAD: true, DefaultFromGuess: false, "": false} {
		if s.Trusted() != want {
			t.Errorf("%q trusted = %v, want %v", s, s.Trusted(), want)
		}
	}
}

func TestDirtyLeavesUntrackedOut(t *testing.T) {
	s := Status{Staged: 1, Unstaged: 2, Conflicts: 1, Untracked: 5}
	if s.Dirty() != 4 {
		t.Errorf("dirty = %d, want 4", s.Dirty())
	}
}
