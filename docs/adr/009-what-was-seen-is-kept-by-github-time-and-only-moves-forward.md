# ADR-009: What was seen is kept by GitHub's time, and only moves forward

## Status

Accepted

## Context

The github columns mark the open issues and pull requests made since the user last looked at a repository. A count alone cannot tell them: one closed and one opened leave it as it was. gh tidy reads when the newest open ones were made, and needs a time per repository to compare them with, kept between starts.

The state file holds what the user chose (the owner, the columns, the pane's height), and the screen writes it whole from the copy it read at start. A time written there by the engine would be overwritten by the screen's next write, and by another gh tidy open at the same time. The local clock may not agree with GitHub's, and the titles shown may be minutes older than the counts.

## Decision

- What was seen is kept in a file of its own, apart from the state and the cache, written only by the engine. Each write reads the file again and keeps, of each repository, the later time seen, and of each owner, the earlier baseline, so that two gh tidy processes, or a holder of an older copy, hide no mark. Times seen only move forward, and a baseline only back; [internal/store/seen.go](../../internal/store/seen.go) is the format.
- A time seen is GitHub's: the newest creation time among the titles the pane showed, never the local clock. A repository not yet looked at takes its owner's baseline: when GitHub answered the first read of that owner's creation times, from the response's own date. With no baseline, or for a value read without the creation times, which items are new is shown as not known, never as none.
- A repository is named by GitHub's number for it, which a rename and a move to another owner keep.
- The time seen moves only when the cursor has rested on the row in the github columns and both the issues and the pull requests were read and shown; the marks stay until the cursor leaves the row. all reads no more of GitHub (ADR-005), so its marks clear only from the owner's own table.
- The titles are held in memory only: never in the cache, the state, the seen file or the log (ADR-002).
- Rejected:
  - Keeping the times in the state file: two writers of one file, each writing its own copy whole, lose each other's times.
  - The local time of looking as the time seen: titles held from a read minutes before would mark later items as seen, and a clock out of step loses or repeats marks.
  - Comparing the counts read before and after: one closed and one opened look like nothing new.

## Consequences

- Two processes writing in the same instant can still lose one time; that row's marks come back until the cursor rests on it again. The file has no lock.
- An issue reopened or moved in from another repository keeps its old creation time and is not marked new.
- A new kind of mark that follows what the user has seen keeps its times the same way: GitHub's clock, merged by the later on write, in the engine.
