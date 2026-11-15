# ADR-005: One engine owns what gh-tidy knows, and reads within limits and a budget

## Status

Accepted

## Context

The prototype kept its data, its concurrency (a semaphore for fetches) and its decisions about reading in the TUI's one model, which every file of the TUI could write; state kept that way has to be moved out part by part later. gh-tidy now reads many clones and one or more owners' lists and fields from GitHub, in the background, under a rate limit shared with other tools, while the user switches owners and changes clones.

## Decision

- [internal/engine](../../internal/engine) is the only holder of what gh-tidy knows of owners and clones, and the only code that changes it. The screen asks it for a copy of the owner shown, which is never changed after it is handed out, and is told of changes through a channel. The engine does not import Bubble Tea.
- The engine reaches the outside world only through interfaces it defines (the clones on disk, the changes to them, GitHub, the files kept between starts); the program's top passes the concrete packages, and tests pass made-up ones. It can be waited on until no work is left, so that tests need no sleeps.
- Each kind of outside access has its own limit on what runs at once: git reading the disk, git talking to GitHub, and requests to GitHub's API. Within a kind, what the user asked for goes first, then the rows on the screen, then the rest. The limits, the batch size and how long values stay fresh are constants: they guard the load on the machine and on GitHub.
- Opening another owner drops the queued reads of the one before and cancels its requests to GitHub; changes in flight go on, and an owner with a change in flight is not let go from memory. A read begun before a change to the same clone started or ended is dropped, so that it cannot put back a state the change has moved past.
- Changes to a clone are made one at a time, in its lane, as ADR-003 decides them.
- What gh-tidy reads on its own is held back by a budget: below a share of the hourly points left, heavy fields stop, and below a smaller share everything does; gh-tidy also caps what it spends unasked in an hour, whatever is left. The lowest remaining count seen for a reset is used, since GitHub's regions report it out of step. After a rate limit refusal no request goes until GitHub says it may, longer each time it happens again. After GitHub gives up on a query, queries ask for fewer repositories.
- The screen tells the engine what it needs read (the union of what its columns and preview need) and which rows it shows; the engine reads only that, the rows shown first. A value records which fields its read asked for, so that a field needed later is read.
- `all` is a view put together from every owner's clones and their GitHub values as last read; it reads no more of GitHub.
- Rejected: keeping data in the TUI's model and moving it out later, each move touching every part that reads it.
- Rejected: one limit for every kind of access. A slow fetch would hold back reading the disk, and the API's limit is set by GitHub's rate limits, not by the machine.

## Consequences

- A new kind of data needs its place in internal/model, its reading in the engine with a freshness rule, and its need declared by what shows it.
- The screen cannot change what is known by writing a field; every change goes through the engine's methods.
- A test of the screen runs the real engine against made-up git and GitHub.
