# ADR-001: Package layers and import rules enforced by a test

## Status

Accepted

## Context

gh-tidy is being rebuilt from a working prototype into a tool meant to last. It is growing parts that tangle easily: reading many clones with git, reading one or more owners' repositories from GitHub under a rate limit shared with other tools, a cache on disk, bulk operations that change clones (pulling, removing worktrees), and a TUI over all of it. The prototype kept most of its state and its concurrency in the TUI's model, so the rules that keep the user's work safe (what `p` refuses, what `d` keeps) could only be tested with real repositories and a model driven by key presses.

Untangling packages after the fact is costly: state that every file of a TUI can write has to be moved out part by part, each move touching everything that reads it. Setting the layers before the parts grow, and checking every import with a test, keeps that from building up.

## Decision

- The code is split into layered packages. A package imports only packages of lower layers and only the outside modules listed for it. The table in [arch_test.go](../../arch_test.go) holds every package with its layer, what it may import and what it does, and [docs/architecture.md](../architecture.md) shows that table.
- The standard library packages that reach outside the process (`net`, `net/http`, `os/exec`, `syscall`) are allowed only in the packages listed for each. Running git and other programs is limited to the packages that own that reach, so that gh-tidy keeps running git and gh only and reaches other tools through the user's settings.
- `arch_test.go` reads `go list -json ./...` and fails on an import outside the table, on a package missing from the table, on an allowed import that is not made, and on a table entry that points to the same or a higher layer. It also fails when docs/architecture.md differs from the table; `go test -run TestArchitectureDoc -update` rewrites the document.
- Interfaces are defined by the package that uses them, and the package that puts the program together passes the concrete types.
- No package keeps state in package variables, and `slog.SetDefault` is not used.
- The table describes the code as it is, including the prototype's packages while they are replaced. A stage of the rebuild changes the table in the same change as the code.
- Rejected: directories that only group packages by layer (`internal/core`, `internal/infra`). They would state the layers a second time beside the table, and nothing would keep the two in step.

## Consequences

- A new package, a new outside module or a new use of `os/exec` is a visible change to `arch_test.go` and docs/architecture.md, not an accident.
- The rules that decide what to do with the user's work can live in a package that reaches nothing outside the process, and be tested without git or a terminal.
- Some values (the logger, the config) are passed down explicitly where a global would be shorter.
- Test files are not checked, so a test may import any helper.
