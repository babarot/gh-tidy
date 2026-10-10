# gh-fleet

A gh extension that lists one GitHub owner's local clones with what each has to say: how far it is from its upstream and its default branch, what is not committed or not pushed, and its worktrees. From the list you can fetch, pull, clone what is not cloned yet, and remove the worktrees and branches you are done with.

```
 fleet // babarot   cloned 61  not cloned 31                     38 need attention · 20 stale · sort: attention
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
  REPO                          BRANCH                  STATUS         SYNC     BASE    BRANCHES     FETCHED
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
▎ enter-rs local only           main                    4%                                           -
  github-config                 main                    5* 3+ 1$       >1?                           never
  gh-infra ⌥3                   babarot/followup-secr…  3% 2$          =?               ↑1 ·2        May 20
  agent-recall → claude-recall  babarot/settle-size                    =?               ↑2           6d ago
  herdr                         patches                                =        ⇣109    ·4           2d ago
  gist                          main                                   <12                           4d ago
```

## Install

```sh
gh extension install babarot/gh-fleet
```

## Usage

```sh
gh fleet            # the owner of where you are
gh fleet babarot    # one owner
gh fleet ghe.example.com/team
```

Clones are looked for where [ghq](https://github.com/x-motemen/ghq) puts them, `<root>/<host>/<owner>/<repo>`; the root is `ghq.root` in git config, or `~/ghq`. ghq itself is not needed. Without an owner, gh fleet works it out from the directory it starts in:

| Started in | Opens |
|---|---|
| a clone or one of its worktrees, wherever the worktree is | the owner of its origin, with that clone selected |
| `<root>/<host>/<owner>` | that owner; with nothing cloned yet, the repositories to clone |
| `<root>` or `<root>/<host>` | the owners to pick from |
| anywhere else | `default_owner`, or the owners to pick from |

A clone is matched to GitHub by its origin, not its directory name, so a clone in a directory of another name, or of a repository renamed since, is still found.

## What the columns say

| Column | Shows |
|---|---|
| REPO | `⌥N` N worktrees; `local only` no origin, never pushed; `→ name` the origin is another repository; `archived` |
| STATUS | `3*` unstaged, `1+` staged, `12%` untracked, `2$` stashed; `status_style = "symbol"` writes `*+%$` as [zsh-mini-prompt](https://github.com/babarot/zsh-mini-prompt) does |
| SYNC | against the upstream: `<N` behind, `>N` ahead, `<N>M` diverged, `=` the same |
| BASE | `⇣N`: the default branch has N commits the current branch does not, time to rebase |
| BRANCHES | the other branches: `↑N` with commits not pushed, `·N` tracking nothing, `✗N` whose upstream was deleted |
| FETCHED | when the clone last fetched |

Only what is on your machine is read, so the counts are as of the last fetch. To know when they are out of date without fetching, gh fleet asks GitHub, in one query, where each clone's default branch is now; when GitHub has moved past the clone, SYNC and BASE get a `?` and FETCHED turns yellow. `F` fetches everything.

Repositories with nothing to say, and those whose only news is untracked files, are hidden until `a`.

## Keys

| Key | Does |
|---|---|
| `enter` | show the clone's worktrees under it |
| `f` / `F` | fetch the clone / every clone, a few at a time |
| `p` | `git pull --ff-only`; refused, with the reason, on uncommitted changes, a diverged branch or no upstream |
| `P` | fetch everything, then pull the clones that are behind with nothing in the way |
| `d` | remove merged worktrees and branches (below) |
| `o` | open on GitHub |
| `R` | reveal in Finder |
| `y` / `Y` | copy the path / the URL |
| `tab` | the owner's repositories not cloned yet; `c` clones one |
| `/` | filter by name or branch |
| `a` | show the hidden repositories |
| `s` | sort by attention, name, last fetch or last commit |
| `?` | every key |

### Cleaning up

`d` measures the selected clone against `origin/<default>` and lists what it would remove and what it keeps, with why; nothing is removed until `enter`.

- A worktree goes when its branch is merged, it has no changes, and it was committed to after it was made. A worktree never committed to is kept: its branch sits on the default branch, so it looks merged, but it may be the one you are about to work in. So is the worktree gh fleet runs in.
- A worktree whose directory is gone is pruned.
- A merged branch checked out nowhere goes.
- A branch whose upstream was deleted but which is not merged, as after a squash merge, is kept unless `delete_gone = true`.

## Settings

`~/.config/gh-fleet/config.toml`, written on first run with every setting commented out at its default. A mistake is reported with where it is in the file.

### Handing work to other tools

gh fleet runs git and gh, and nothing else on its own. Two settings let it hand work to other tools.

`[[actions]]` binds a key to a command run on the selected clone or worktree:

```toml
[[actions]]
key = "H"
name = "open in herdr"
scope = "worktree"   # "repo" (default) or "worktree"
run = ["herdr", "worktree", "open", "--path", "{path}"]
```

`{path}`, `{repo}` (owner/name), `{owner}`, `{name}`, `{branch}`, `{default}` and `{url}` are filled in; each element of `run` stays one argument. The last line the command prints shows in the status line; `interactive = true` gives it the terminal instead. The clone is read again afterwards, and dropped from the list when the command removed it. `confirm = true` shows the command and asks before running it, for one that removes something:

```toml
[[actions]]
key = "D"
name = "trash"
confirm = true
run = ["gomi", "{path}"]
```

`worktree_remove` is how `d` removes a worktree, for worktrees another tool made and should forget too. The command runs in the clone and must remove the worktree at `{path}`; a script can look up what the tool needs from it:

```toml
[cleanup]
worktree_remove = ["remove-worktree", "{path}"]
```

`d` still decides what goes; the command only removes it.

## License

MIT
