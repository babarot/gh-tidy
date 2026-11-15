// Package logging makes the logger gh-tidy writes its log file with: JSON
// lines through log/slog, to a file under the state directory, never to the
// terminal the TUI draws on.
//
// The logger is made once, at the top of the program, and passed down;
// packages add what they know with With (an operation, an owner, a
// repository) and never keep a logger of their own in a package variable.
package logging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Attribute keys every package uses, so a log line can be searched by them.
const (
	KeyRun     = "run"
	KeyVersion = "version"
	KeyOp      = "op"
	KeyOwner   = "owner"
	// KeyRepo names a repository as host/owner/name, or a clone by its path.
	KeyRepo         = "repo"
	KeyDurationMS   = "duration_ms"
	KeyGHRequestID  = "gh_request_id"
	KeyGQLCost      = "gql_cost"
	KeyGQLRemaining = "gql_remaining"
	// KeyWaitedMS is how long a request waited for the rate limit, and
	// KeyReason why something was held back or skipped.
	KeyWaitedMS = "waited_ms"
	KeyReason   = "reason"
)

// Rotation: the file is moved aside when it is over maxSize at startup,
// and keep old files are kept. Constants, not settings: they bound the disk
// a log may take, which no user needs to tune.
const (
	maxSize = 10 << 20
	keep    = 3
)

// DefaultPath is where the log file of the program called name goes:
// $XDG_STATE_HOME/<name>/logs/<name>.log, or under ~/.local/state.
func DefaultPath(name string) string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, name, "logs", name+".log")
}

// ParseLevel reads a level as written in a flag or the environment:
// debug, info, warn or error. Empty is info.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("%q is not a log level: debug, info, warn or error", s)
}

// Open moves the log file at path aside if it has grown too large, opens it
// for appending with mode 0600, and returns a logger writing JSON lines to
// it at level and above. Closing the returned closer closes the file.
func Open(path string, level slog.Leveler) (*slog.Logger, io.Closer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	if err := rotate(path, maxSize, keep); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return New(f, level), f, nil
}

// New returns a logger writing JSON lines to w at level and above. Tests
// pass a buffer.
func New(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// Discard is a logger that writes nothing, for code run without one.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// rotate moves path to path.1, path.1 to path.2 and so on, dropping the
// oldest beyond keep, when path is larger than max.
func rotate(path string, max int64, keep int) error {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= max {
		return nil
	}
	old := func(n int) string { return fmt.Sprintf("%s.%d", path, n) }
	if err := os.Remove(old(keep)); err != nil && !os.IsNotExist(err) {
		return err
	}
	for n := keep - 1; n >= 1; n-- {
		if err := os.Rename(old(n), old(n+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(path, old(1))
}

// NewID returns a short random ID, for a run or an operation.
func NewID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}
