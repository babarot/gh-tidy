package statusline

import (
	"testing"
	"time"
	"unicode/utf8"
)

func TestLine(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var l Line
	if _, ok := l.Current(now); ok {
		t.Fatal("a message before any was said")
	}
	n := l.Say(Done, "fetched app", now)
	if m, ok := l.Current(now.Add(time.Second)); !ok || m.Text != "fetched app" || m.Kind != Done {
		t.Errorf("current = %+v, %v", m, ok)
	}
	if _, ok := l.Current(now.Add(For)); ok {
		t.Error("the message outlived For")
	}
	l.Say(Failed, "clone failed", now)
	l.Expire(n) // the first one's timer: the second stays
	if m, _ := l.Current(now); m.Text != "clone failed" {
		t.Errorf("an older timer took a newer message: %+v", m)
	}
	l.Hold(Working, "quitting once 1 change ends")
	if _, ok := l.Current(now.Add(time.Hour)); !ok {
		t.Error("a held message went")
	}
}

func TestSpinnerAndBar(t *testing.T) {
	t0 := time.Unix(0, 0)
	seen := map[string]bool{}
	for i := range len(frames) {
		f := Spinner(t0.Add(time.Duration(i) * FrameEvery))
		if utf8.RuneCountInString(f) != 1 {
			t.Errorf("frame %q is not one character", f)
		}
		seen[f] = true
	}
	if len(seen) != len(frames) {
		t.Errorf("%d frames seen, want %d", len(seen), len(frames))
	}
	for _, tc := range []struct{ done, total, w, want int }{{0, 10, 10, 0}, {5, 10, 10, 5}, {10, 10, 10, 10}, {3, 0, 10, 0}, {12, 10, 10, 10}} {
		if got := Bar(tc.done, tc.total, tc.w); got != tc.want {
			t.Errorf("Bar(%d, %d, %d) = %d, want %d", tc.done, tc.total, tc.w, got, tc.want)
		}
	}
}
