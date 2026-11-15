# AGENTS.md

gh-tidy keeps one GitHub owner's local clones up to date and clear of finished work, so they are ready to hand to a coding agent. It is a gh extension with a Bubble Tea TUI. The README tells what it does; this file tells how it is built.

## Packages

- The packages, their layers and what each may import are in [docs/architecture.md](docs/architecture.md), checked by `arch_test.go`. A new package, outside module, or use of `net`, `net/http`, `os/exec` or `syscall` goes into the table in `arch_test.go` first; then `go test -run TestArchitectureDoc -update` rewrites the document. See [ADR-001](docs/adr/001-package-layers-enforced-by-a-test.md)
- Define an interface in the package that uses it. No package variables holding state, and no `slog.SetDefault`

## Decisions

- Record a decision that sets the direction of the code as an ADR in `docs/adr/`, following `.claude/rules/adr.md`

## Rules

- Read only what is on the machine unless the user asks (`f`, `F`, `p`, `P`, `c`). GitHub is asked only for what git cannot know: where the default branch is now, and what is not cloned.
- Asking GitHub for `defaultBranchRef` while listing an owner's repositories, even its `name` alone, takes 25 seconds for 400 repositories instead of 6 (measured on 2026-11-14). Keep it in the lookup by name, batched with aliases, not in the list.
- Match clones to GitHub by the origin URL, never by the directory name.
- `d` never forces anything onto the user's work: a worktree with changes, one never committed to, the one gh tidy runs in and a branch not merged into `origin/<default>` stay unless a setting says otherwise. Re-check that a branch is merged right before deleting it.
- gh tidy runs git and gh only. Other tools are reached through `[[actions]]` and `[cleanup] worktree_remove`, never by name in the code.
- A new setting goes in `config.File`, `Default`, `validate` and `Template`; `TestTemplateIsDefault` checks the template uncommented decodes to the defaults. A new key goes in `defaultKeys` and the `[keys]` part of the template.

## Logging

- Log through the `*slog.Logger` passed in, with the attribute keys in `internal/logging`. A change to a clone is logged at info, a read at debug. Never log tokens, file contents, diffs, commit messages, or issue and pull request titles and bodies. See [ADR-002](docs/adr/002-structured-logging-to-a-file.md)

## Tests

`make check` must pass before a change lands in main: golangci-lint (pinned in `mise.toml`), `go mod tidy -diff`, gofmt and `go test -race ./...`. CI runs the same.

Tests build real repositories with git in a temporary directory (`internal/gittest`): a bare remote, clones of it, worktrees. The TUI tests drive the model with key presses and read the screen with ANSI stripped; the model's timers are off in tests, so every command returns. `TIDY_LIVE=1 go test ./internal/github/` asks GitHub for real; with `TIDY_LIVE_ORG=<org>` it also reads an organization and prints counts only. The other GitHub tests run against a made-up GitHub behind the client's transport (`internal/github/fake_test.go`).
