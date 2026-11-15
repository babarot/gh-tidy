# ADR-002: Structured logging to a file with slog

## Status

Accepted

## Context

gh-tidy changes clones the user works in (pulling, removing worktrees and branches), many at a time, and reads GitHub under a rate limit it shares with other tools. When something goes wrong or is slow, it must be possible to tell afterwards what was done to which clone, what was read from GitHub at what cost, and why gh-tidy waited or held back a read, without reproducing it. The TUI owns the terminal, so nothing can be printed there.

The repository is public, and users will paste logs into issues, so a log must not carry anyone's work.

## Decision

- Log with the standard library's `log/slog`, as JSON lines, to a file under `$XDG_STATE_HOME/gh-tidy/logs/` (or `~/.local/state`), mode 0600. Nothing is logged to stdout or stderr while the TUI runs. `gh tidy --help` says where the file is.
- The file is moved aside at startup when it has grown past a size, and a few old files are kept. Both are constants: they bound the disk a log may take.
- The level is info unless `--debug` or `GH_TIDY_LOG_LEVEL` says otherwise. A log that cannot be opened is said on stderr and left out; gh-tidy works without one.
- The logger is built at the top of the program, carries `run` (an ID for the process), and is passed down. Packages add their own attributes with `With`. The attribute keys are constants in [internal/logging](../../internal/logging). No package keeps a logger in a package variable, and `slog.SetDefault` is not used.
- Every change gh-tidy makes (a fetch, a pull, a branch or worktree removed, a clone, a user's action run) is logged at info with the clone and the result. Reads are logged at debug. Calls to GitHub log their request ID, cost and the rate limit left. Waiting for the rate limit, holding back automatic reads, and resuming them are logged at info with the reason.
- Never logged: tokens, the contents of files, diffs, commit messages, and the titles or bodies of issues and pull requests. A repository is named as `host/owner/name` and a clone by its path.
- Rejected: logging to the terminal behind a flag. The TUI draws the whole screen, and a second writer would break it.

## Consequences

- A user's report can carry the log, and the log can be read without seeing anyone's code or discussions.
- Questions about a commit or an issue need the repository itself, not the log.
- A new kind of change or of GitHub call must log at the level above, with the keys from internal/logging.
