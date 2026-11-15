# ADR-006: Popups declare their parts and share one way of reading keys

## Status

Accepted

## Context

gh-tidy's popups (choosing an owner, the confirms before `P`, `d`, `D` and an action, the list of keys) each read their keys in their own code and wrote their hints by hand. The confirm wrote "esc" itself, the list of keys closed on `?` and `q`, a single click chose an item, and a key changed under `[keys]` left the hints as they were. Each popup added would have read esc, the cursor and the confirm a little differently again.

## Decision

- A popup declares the parts it holds: a list to choose from (which typing may narrow), a body to read, a question to confirm. [internal/tui/popup.go](../../internal/tui/popup.go) reads the keys and the mouse the same way for every popup; a popup does not read keys of its own.
- Keys are read by action from the keymap; no key is written in the popup code. The hints inside a popup and on the key line under it come from what it holds and the keymap, so that a key changed under `[keys]` changes its hint. The key line under a popup names only the keys that change something, and esc.
- esc steps back one level: it clears what was typed, then closes. The key that opened a popup does not close it, and quit is not read in a popup.
- One popup shows at a time. It opens a quarter down the screen, or in the middle when it is too tall for that, and stays where it opened while what it holds changes.
- The mouse in a popup moves and scrolls; a click puts the cursor on an item and a click on the item the cursor is on chooses it; a click on what a confirm says does nothing, so no change runs by mouse; a click outside closes. Nothing reaches the screen under a popup.
- The popup's widths are constants: they set a layout, which no user needs to tune.
- Rejected: keeping each popup's keys in its own code. Every popup added would read esc, the cursor and the confirm a little differently, and hints written by hand drift from `[keys]`.

## Consequences

- A new popup is a set of parts. Keys of its own need a part for them in the shared layer, not a switch in the popup.
- A test checks every popup reads the keys alike ([internal/tui/tui_test.go](../../internal/tui/tui_test.go)); a popup added goes into its table.
