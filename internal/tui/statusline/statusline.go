// Package statusline holds what the status line says: a message for a
// moment, of a kind, and the spinner and bar that show work in progress.
// What must be found again later does not belong here; the rows keep the
// results of changes until they are put away.
package statusline

import "time"

// Kind is what a message says, which sets its color.
type Kind int

const (
	Info    Kind = iota // a fact, said in passing
	Working             // something started, which goes on
	Done                // something done
	Refused             // something not done, and why
	Failed              // something that went wrong
)

// For is how long a message stays. A constant: the status line is for the
// moment, and nobody needs to tune how long a moment is.
const For = 5 * time.Second

// Message is what the status line says.
type Message struct {
	Kind  Kind
	Text  string
	Until time.Time // zero: until something else is said
}

// Line is the status line's message.
type Line struct {
	msg Message
	seq int
}

// Say puts a message up until now+For, or until another is said, and
// returns its number, for Expire.
func (l *Line) Say(k Kind, text string, now time.Time) int {
	l.seq++
	l.msg = Message{Kind: k, Text: text, Until: now.Add(For)}
	return l.seq
}

// Hold puts a message up until something else is said.
func (l *Line) Hold(k Kind, text string) {
	l.seq++
	l.msg = Message{Kind: k, Text: text}
}

// Expire takes message n down, unless another was said since.
func (l *Line) Expire(n int) {
	if n == l.seq {
		l.msg = Message{}
	}
}

// Clear takes any message down.
func (l *Line) Clear() { l.msg = Message{} }

// Current is the message up at now.
func (l *Line) Current(now time.Time) (Message, bool) {
	if l.msg.Text == "" || (!l.msg.Until.IsZero() && !now.Before(l.msg.Until)) {
		return Message{}, false
	}
	return l.msg, true
}

// frames are the spinner's, each one cell wide.
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// FrameEvery is how often the spinner turns, and how often a screen that
// shows it redraws.
const FrameEvery = 90 * time.Millisecond

// Spinner is the spinner's frame at t. Taken from the clock, so every
// spinner on the screen agrees; there is one place it turns.
func Spinner(t time.Time) string {
	return frames[int(t.UnixMilli()/FrameEvery.Milliseconds())%len(frames)]
}

// Bar is how many of width cells a bar of done out of total fills: only
// what is done, so it never runs ahead.
func Bar(done, total, width int) int {
	if total <= 0 || width <= 0 {
		return 0
	}
	return min(width, done*width/total)
}
