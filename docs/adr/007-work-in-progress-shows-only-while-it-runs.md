# ADR-007: Work in progress shows only while it runs, with one spinner

## Status

Accepted

## Context

gh-tidy reads clones and GitHub in the background, and fetches, updates, cleans up and clones in parallel on a key. The screen said little of it: a row read was drawn when it came, and a slow load looked the same as a stuck one. The engine now hands out what a run is doing ([internal/model/activity.go](../../internal/model/activity.go)): the counts, the tasks running and done, the workers, and what GitHub's rate limit has left.

Showing all of it costs lines the list needs, and most of the time nothing runs. A log of a few lines kept on the screen would take a fifth of the list on a common terminal, and between runs it would show only old lines. Several spinners turning on their own clocks read as noise.

## Decision

- The screen shows the work of a run only while it runs, and for a short constant time after, then gives the lines back to the list. Nothing about work in progress takes lines when nothing runs.
- Everything that turns on the screen turns on one clock, which ticks only while something is shown that moves or waits to fold; tests stop it. Where the work is shown in a box, the status line does not show it too; the status line shows the progress only when the screen is too short for the box.
- What GitHub's rate limit has left is shown as one number, the lower of REST's and GraphQL's, and only while gh tidy asks GitHub's API and for a while after. Which resource it is goes to the log. What gh tidy spent is shown as its own count, never added to what is left, since the limit is shared with gh and other tools.
- How many workers run is shown only when asked for with a key, and only while work runs. It explains a slow run; it is not something to watch.
- The words shown for a task are the engine's few words of how it went, never an error's own text, which can hold a remote's address.
- How long the box stays and how many tasks it shows are constants: they set a layout, which no user needs to tune.
- Rejected:
  - A log of a few lines always on the screen: it takes the list's lines when nothing runs and then shows only old lines.
  - The progress and the rate limit in the header: the header says what the owner's clones need, and work in progress would push it out while it runs.
  - One line per repository, rewritten as it goes: with hundreds of clones it would need its own pane, and a pane does not fit under the preview.
  - Both resources of the rate limit side by side: looking at the screen, what matters is how much is left; the resource is for the log.

## Consequences

- A new kind of background work shows on the screen through the engine's activity, with a kind and a count; the TUI does not track work of its own.
- Anything new that moves on the screen uses the one clock, and keeps it ticking only while it needs to.
- The list's height changes while a run goes on; whatever maps screen lines to rows (the mouse, paging) reads the layout as last drawn.
