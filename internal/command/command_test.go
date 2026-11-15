package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	out, err := Run(context.Background(), dir, []string{"sh", "-c", "pwd; echo done"})
	if err != nil || !strings.HasSuffix(out, "done") {
		t.Fatalf("Run = %q, %v", out, err)
	}
	_, err = Run(context.Background(), dir, []string{"sh", "-c", "echo one; echo 'it broke' >&2; exit 3"})
	if err == nil || err.Error() != "sh: it broke" {
		t.Errorf("err = %v", err)
	}
	if _, err := Run(context.Background(), dir, nil); err == nil {
		t.Error("no command ran")
	}
}

func TestInteractive(t *testing.T) {
	dir := t.TempDir()
	c := NewInteractive(dir, []string{"sh", "-c", "touch made"})
	c.SetStdin(strings.NewReader(""))
	c.SetStdout(os.Stdout)
	c.SetStderr(os.Stderr)
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "made")); err != nil {
		t.Error("not run in its directory")
	}
}
