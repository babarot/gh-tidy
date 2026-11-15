package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, want := DefaultPath("gh-tidy"), "/state/gh-tidy/logs/gh-tidy.log"; got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got, want := DefaultPath("gh-tidy"), "/home/u/.local/state/gh-tidy/logs/gh-tidy.log"; got != want {
		t.Errorf("DefaultPath without XDG_STATE_HOME = %q, want %q", got, want)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"": slog.LevelInfo, "debug": slog.LevelDebug, " WARN ": slog.LevelWarn, "error": slog.LevelError} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) gave no error")
	}
}

func TestOpenWritesJSONLinesPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "gh-tidy.log")
	log, c, err := Open(path, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden")
	log.Info("start", KeyRun, "abcd1234")
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &line); err != nil {
		t.Fatalf("not one JSON line: %q", data)
	}
	if line["msg"] != "start" || line[KeyRun] != "abcd1234" {
		t.Errorf("line = %v", line)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestRotateKeepsAFewOldFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(path, "current, too large")
	write(path+".1", "one")
	write(path+".2", "two")
	if err := rotate(path, 5, 2); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{path + ".1": "current, too large", path + ".2": "one"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(p), got, want)
		}
	}
	for _, p := range []string{path, path + ".3"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after rotating", filepath.Base(p))
		}
	}
	write(path, "small")
	if err := rotate(path, 100, 2); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "small" {
		t.Errorf("a small file was moved: %q", got)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if len(a) != 8 || a == b {
		t.Errorf("NewID = %q, %q", a, b)
	}
}
