package recording

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/babarot/gh-tidy/internal/model"
)

// The world reads as the recording shows it: agent-runner and
// calendar-sync behind GitHub, dotfiles with changes, a worktree done,
// one new issue; and P brings the clones up to date with git alone.
func TestTheWorldAsRecorded(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_AUTHOR_NAME": "Mika",
		"GIT_AUTHOR_EMAIL": "mika@example.com", "GIT_COMMITTER_NAME": "Mika", "GIT_COMMITTER_EMAIL": "mika@example.com", "GIT_ALLOW_PROTOCOL": "file"} {
		t.Setenv(k, v)
	}
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	w, err := Build(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _, _, err := w.start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eng.Open(Owner)
	eng.SetNeeds(model.NeedCounts|model.NeedNewest|model.NeedAlerts|model.NeedCI|model.NeedNotCloned, nil)
	idle := func() {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := eng.Idle(c); err != nil {
			t.Fatal(err)
		}
	}
	idle()
	rows := map[string]model.Row{}
	for _, r := range eng.Owner().Rows {
		rows[string(r.Remote)] = r
	}
	row := func(name string) model.Row { return rows["github.com/"+Owner+"/"+name] }
	if r := row("agent-runner"); !r.Ahead.Has || !r.Ahead.Value.Ahead {
		t.Errorf("agent-runner is not behind GitHub: %+v", r.Ahead)
	}
	if r := row("agent-runner"); r.New.Issues != 1 {
		t.Errorf("agent-runner's new issues = %+v", r.New)
	}
	if r := row("dotfiles"); !r.Local.Has || r.Local.Value.Dirty() == 0 {
		t.Error("dotfiles has no changes")
	}
	if r := row("tool-shed"); r.Clone != "" || !r.Listed.Has {
		t.Errorf("tool-shed = %+v", r)
	}
	var keys []model.CloneKey
	for _, r := range eng.Owner().Rows {
		if r.Clone != "" {
			keys = append(keys, r.Clone)
		}
	}
	eng.Advance(keys)
	idle()
	for _, r := range eng.Owner().Rows {
		if r.Remote == "github.com/"+Owner+"/agent-runner" && r.Ahead.Value.Ahead {
			t.Error("agent-runner is still behind after P")
		}
	}
	for _, r := range eng.Owner().Rows {
		if r.Clone != "" && r.Op.Failed {
			t.Errorf("%s: %s", r.Clone, r.Op.Result)
		}
	}
}
