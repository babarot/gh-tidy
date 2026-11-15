# Architecture

gh-tidy is split into packages by layer. A package imports only packages of lower layers, and only the outside modules listed for it. The table below is what `arch_test.go` checks; `go test -run TestArchitectureDoc -update` rewrites it from the test, and the test fails when the two differ.

<!-- layers:start -->
| Layer | Package | Does | May import | Outside modules |
|---|---|---|---|---|
| 0 | `internal/version` | The release version | - | - |
| 0 | `internal/logging` | The slog logger, the log file and its attribute keys | - | - |
| 0 | `internal/theme` | The color schemes, named by what the TUI uses each color for | - | - |
| 0 | `internal/model` | The values passed between packages: a clone's state, the facts decisions are made from, the keys of rows | - | - |
| 0 | `internal/gittest` | Real git repositories in a test's temporary directory; imported by tests only | - | - |
| 1 | `internal/gitx` | Running git without taking a lock, and parsing its porcelain output | - | - |
| 1 | `internal/policy` | Deciding what to do with a clone from facts: moving its default branch, what d removes, how a worktree is judged, attention | `internal/model` | - |
| 1 | `internal/config` | Reading the config file and reporting its mistakes | `internal/theme` | `github.com/pelletier/go-toml/v2` |
| 2 | `internal/repo` | One clone's state, read from git alone | `internal/gitx` | - |
| 2 | `internal/local` | A clone's state and the facts about it, read from git, and the changes made to it with git | `internal/gitx`, `internal/model` | - |
| 2 | `internal/github` | Reading GitHub through gh's authentication: owners' lists by REST with conditional requests, repositories' fields by GraphQL, the rate limit of every response | `internal/logging`, `internal/model` | `github.com/cli/go-gh/v2` |
| 3 | `internal/locate` | The root, and what to open from where gh tidy starts | `internal/gitx`, `internal/repo` | - |
| 3 | `internal/cleanup` | What d removes and keeps, and removing it | `internal/config`, `internal/gitx`, `internal/repo` | - |
| 4 | `internal/tui` | The terminal UI | `internal/cleanup`, `internal/config`, `internal/github`, `internal/gitx`, `internal/locate`, `internal/repo`, `internal/theme` | `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `github.com/charmbracelet/x/ansi` |
| 5 | `main` | Flags, config and starting the TUI | `internal/logging`, `internal/config`, `internal/locate`, `internal/repo`, `internal/tui`, `internal/version` | `charm.land/bubbletea/v2` |

Standard library packages that reach outside the process:

| Package | Only in |
|---|---|
| `net` | - |
| `net/http` | `internal/github` |
| `net/rpc` | - |
| `net/smtp` | - |
| `os/exec` | `internal/cleanup`, `internal/gittest`, `internal/gitx`, `internal/tui` |
| `syscall` | - |
<!-- layers:end -->

## Rules

- Each part of the outside world belongs to as few packages as possible. git is run by `internal/gitx`, GitHub is reached by `internal/github`, and the terminal is drawn by `internal/tui`. The standard library packages that reach outside the process are limited to the packages listed for them above.
- Interfaces are defined where they are used, and the package that puts the program together passes the concrete types.
- No package keeps state in package variables. The logger, the config and the clients are built at the top and passed down as arguments and fields. `slog.SetDefault` is not used.

The table describes the code as it is. The rebuild moves code into new packages stage by stage; each stage changes the table in the same change as the code.

## Decisions

Decisions that set the direction of the code are recorded as ADRs in [docs/adr](adr/).
