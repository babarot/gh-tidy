package store

import (
	"path/filepath"
	"time"
)

// SeenVersion is the format of the seen file. A file of another version is
// read as none.
const SeenVersion = 1

// Seen is what the user has seen of each repository's open issues and
// pull requests, by GitHub's clock: the newest made of those shown, by
// repository (host and GitHub's number for it, which a rename keeps),
// and for each owner (host and lower-case login) when its creation times
// were first read, the time seen of a repository not yet looked at. The
// times seen only move forward, and a baseline only back (ADR-009).
type Seen struct {
	Version int
	Repos   map[string]time.Time
	Owners  map[string]time.Time
}

func (s Store) seenPath() string { return filepath.Join(s.StateDir, "seen.json") }

// LoadSeen returns what was seen, or nothing.
func (s Store) LoadSeen() (Seen, error) {
	var sn Seen
	ok, err := readJSON(s.seenPath(), &sn)
	if err != nil || !ok || sn.Version != SeenVersion {
		sn = Seen{}
	}
	if sn.Repos == nil {
		sn.Repos = map[string]time.Time{}
	}
	if sn.Owners == nil {
		sn.Owners = map[string]time.Time{}
	}
	return sn, err
}

// SaveSeen keeps what sn adds: the file is read again and each time seen
// kept is the later of the two, each baseline the earlier, so that
// another gh tidy writing meanwhile, or a holder of an older copy, hides
// no mark. It returns the merged times.
func (s Store) SaveSeen(sn Seen) (Seen, error) {
	cur, _ := s.LoadSeen()
	for k, t := range sn.Repos {
		if t.After(cur.Repos[k]) {
			cur.Repos[k] = t
		}
	}
	// An owner's baseline keeps the earlier: a later one, set by another
	// gh tidy started meanwhile, would hide what came between.
	for k, t := range sn.Owners {
		if old, ok := cur.Owners[k]; !ok || t.Before(old) {
			cur.Owners[k] = t
		}
	}
	cur.Version = SeenVersion
	return cur, writeJSON(s.seenPath(), cur)
}
