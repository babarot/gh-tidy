// Package store keeps on disk what gh-tidy shows at the next start: each
// owner's table as last read (the cache, which may be deleted at any time)
// and the choices the user made (the state: the owner last chosen, the
// columns, the preview's height).
//
// Files are written through a temporary file renamed into place, with mode
// 0600, and carry a format version; a file of another version, host or
// account is not used, and is replaced at the next write.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

// Version is the format of the files. A file of another version is read
// as no file.
const Version = 2

// Store is where the files go: CacheDir for the cache and StateDir for the
// state, each already under the program's own directory.
type Store struct {
	CacheDir string
	StateDir string
}

// OwnerCache is one owner's table as last read, for one account on one
// host.
type OwnerCache struct {
	Version int
	Host    string
	Viewer  string
	Owner   string

	List    model.Completeness
	ListWhy string
	ListAt  time.Time
	// ETags and Ended are what asking the list again conditionally needs.
	ETags []string
	Ended bool

	Rows    []model.Row
	SavedAt time.Time
}

func (s Store) ownerPath(host, viewer, owner string) string {
	return filepath.Join(s.CacheDir, safe(host), safe(viewer), "owners", safe(owner)+".json")
}

// LoadOwner returns what was kept of owner, or false when nothing usable
// was: no file, or one of another version, host or account.
func (s Store) LoadOwner(host, viewer, owner string) (OwnerCache, bool, error) {
	var c OwnerCache
	ok, err := readJSON(s.ownerPath(host, viewer, owner), &c)
	if err != nil || !ok {
		return OwnerCache{}, false, err
	}
	if c.Version != Version || c.Host != host || !strings.EqualFold(c.Viewer, viewer) || !strings.EqualFold(c.Owner, owner) {
		return OwnerCache{}, false, nil
	}
	return c, true, nil
}

// SaveOwner keeps c, replacing what was kept of its owner.
func (s Store) SaveOwner(c OwnerCache) error {
	c.Version = Version
	return writeJSON(s.ownerPath(c.Host, c.Viewer, c.Owner), c)
}

// State is what the user chose, kept between starts.
type State struct {
	Version int
	// LastOwner is the owner last chosen, by host and account
	// ("github.com/babarot"): another account starts from its own.
	LastOwner     map[string]string
	Lens          string
	PreviewHeight int
}

func (s Store) statePath() string { return filepath.Join(s.StateDir, "state.json") }

// LoadState returns the state kept, or an empty one.
func (s Store) LoadState() (State, error) {
	st := State{LastOwner: map[string]string{}}
	ok, err := readJSON(s.statePath(), &st)
	if err != nil || !ok || st.Version != Version {
		return State{Version: Version, LastOwner: map[string]string{}}, err
	}
	if st.LastOwner == nil {
		st.LastOwner = map[string]string{}
	}
	return st, nil
}

// SaveState keeps st.
func (s Store) SaveState(st State) error {
	st.Version = Version
	return writeJSON(s.statePath(), st)
}

// AccountKey is how LastOwner names an account on a host.
func AccountKey(host, viewer string) string { return host + "/" + strings.ToLower(viewer) }

func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if json.Unmarshal(data, v) != nil {
		return false, nil // a file cut short or from elsewhere: as if none
	}
	return true, nil
}

// writeJSON writes v to path through a temporary file of its own, renamed
// into place, so that a reader never sees half a file and two writers
// never mix theirs.
func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// safe makes a host, account or owner name usable as one path element.
func safe(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(s)
}
