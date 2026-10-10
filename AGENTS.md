# AGENTS.md

gh-tidy keeps one GitHub owner's local clones up to date and clear of finished work, so they are ready to hand to a coding agent. It is a gh extension with a Bubble Tea TUI. The README tells what it does; this file tells how it is built.

## Layout

| Package | Holds |
|---|---|
| `internal/gitx` | running git (never taking a lock: `GIT_OPTIONAL_LOCKS=0`) and parsing its porcelain output |
| `internal/repo` | one clone's state, read from git alone; never fetches |
| `internal/locate` | the root (`ghq.root`), and what to open from where gh tidy starts |
| `internal/github` | the two GraphQL queries: the clones' default branches by name, and the owner's list |
| `internal/cleanup` | what `d` removes and keeps, and removing it |
| `internal/config` | `config.toml`: defaults, the template written on first run, validation |
| `internal/tui` | the list, the detail pane, the dialog, keys |

## Rules

- Read only what is on the machine unless the user asks (`f`, `F`, `p`, `P`, `c`). GitHub is asked only for what git cannot know: where the default branch is now, and what is not cloned.
- Asking GitHub for `defaultBranchRef { target { oid } }` while listing an owner's repositories takes half a minute for 400 repositories. Keep it in the lookup by name, batched with aliases, not in the list.
- Match clones to GitHub by the origin URL, never by the directory name.
- `d` never forces anything onto the user's work: a worktree with changes, one never committed to, the one gh tidy runs in and a branch not merged into `origin/<default>` stay unless a setting says otherwise. Re-check that a branch is merged right before deleting it.
- gh tidy runs git and gh only. Other tools are reached through `[[actions]]` and `[cleanup] worktree_remove`, never by name in the code.
- A new setting goes in `config.File`, `Default`, `validate` and `Template`; `TestTemplateIsDefault` checks the template uncommented decodes to the defaults. A new key goes in `defaultKeys` and the `[keys]` part of the template.

## Tests

Tests build real repositories with git in a temporary directory (`internal/gittest`): a bare remote, clones of it, worktrees. The TUI tests drive the model with key presses and read the screen with ANSI stripped; the model's timers are off in tests, so every command returns. `TIDY_LIVE=1 go test ./internal/github/` asks GitHub for real.
