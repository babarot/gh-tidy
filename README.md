# gh-tidy

A gh extension that keeps one GitHub owner's local clones up to date and clear of what you are done with, so they are ready to hand to a coding agent.

![gh tidy updating an owner's clones, cleaning up a finished worktree, and showing alerts and new issues on GitHub](docs/demo.gif)

A coding agent works from the clone it is given. If the clone's default branch is behind GitHub's, the agent starts its work from old code, or reads old code as context and decides on it. gh tidy lists an owner's clones with what stands in the way, moves their default branches forward where nothing is in the way, and removes the worktrees and branches you are done with.

```
 tidy // ‹ mika ›    local │ github                              4 can pull · 4 with changes · 1 worktree done
  REPO                        MAIN            CHECKED OUT                   CHANGES         WORKTREES
▎ agent-runner ⌥3             <6              retry-backoff >2 ⇣5           *1              1 busy  1 new  1 done
  scratchpad local only       no origin       main                          %4
  dotfiles                    <2              main                          *3 %2 $1
  calendar-sync               <1              feat/ics-export = ⇣1          *1 +2
  trail-notes                 <?              main
  ▸ nothing to do  2
  ▸ not cloned  2   c clones one
```

## Install

```sh
gh extension install babarot/gh-tidy
```

gh tidy reads github.com only; GitHub Enterprise Server is not supported.

## Usage

```sh
gh tidy            # the owner of where you are
gh tidy babarot    # one owner
gh tidy all        # every owner's clones
```

Clones are looked for as `<root>/<host>/<owner>/<repo>`, under the roots `[core] roots` names, such as `["~/src", "~/work"]`. With none set, gh tidy offers the root of the clone it starts in and writes it to a new config file when you say yes; otherwise, and always for `--json`, it stops with the line to write. Without an owner, gh tidy opens the owner of the clone it starts in (with that clone selected), the owner directory it starts in, the owner opened last, or you.

A clone is matched to GitHub by its origin, not its directory name, so a clone in a directory of another name, or of a repository renamed since, is still found. A worktree is shown under its clone wherever it is on disk.

`<` and `>` step through the owners, and `O` chooses one: you, your organizations, the owners you have clones of, and `all`. For you and your organizations, gh tidy reads the list of repositories, so those not cloned show at the end. For any other owner it looks up only what you have cloned, so a large organization costs nothing until you press `L`.

## The list

Each row is a clone, the ones that need looking at first. Clones with nothing to do and repositories not cloned (neither forks nor archived) are folded at the end; `l` or a click on a head opens it, and `h` folds it again. `l` on a clone shows its linked worktrees under it, and `h` hides them.

The marks are those of [starship](https://starship.rs) by default, and each one is compared with one thing on every row:

| Mark | Means |
|---|---|
| `⇣N` `⇡N` `⇣N⇡M` `=` | behind, ahead, diverged from, or the same as the upstream. In MAIN, the local default branch against `origin/<default>` |
| `?` | in MAIN, GitHub's default branch has commits you have not fetched; what they are is known after `f` |
| `⇠N` | the default branch has N commits the branch checked out lacks: time to rebase |
| `!` `+` `?` `$` | unstaged, staged, untracked, stashed |
| `⌥N` | N linked worktrees |
| `⋅` | not read yet |

`style = "classic"` under `[symbols.branch]` or `[symbols.changes]` in the config file switches that set to the marks of [zsh-mini-prompt](https://github.com/babarot/zsh-mini-prompt) (`<` `>` `⇣`, `*` `+` `%` `$`), and each mark can be set on its own.

WORKTREES judges each linked worktree: `busy` (changes, unpushed commits, or touched lately), `new` (never committed to: about to be used), `done` (merged and clean: `d` removes it), `idle` (clean, not merged, untouched for two weeks), with how long ago it was made or last touched. `+N br` counts branches in no worktree that hold work found nowhere else.

`v` switches to the github columns: open issues and pull requests (`4 +1` when one was made since you last rested on the row and read its titles), Dependabot alerts (`off` when they are not turned on, `n/a` when your token may not read them), the checks on the default branch, its stars, and when it was last pushed. They are ordered by what needs attention on GitHub, and the repositories not cloned are mixed in.

The pane below the list shows what is behind the selected row's marks: the commits `p` would bring in, those not pushed, those on the default branch the branch lacks, the files changed and the stash; on the right, where the row is and what `p` and `d` would do to it now. `+` and `-`, or dragging its top line, change its height.

gh tidy reads GitHub on its own only within a share of your rate limit, leaving the rest to your other tools, and keeps what it read so the next start shows it at once.

## Keys

| Key | Does |
|---|---|
| `l` `h` | open or close: a folded section, or the clone's worktrees |
| `v` | local / github columns |
| `<` `>` `O` | previous, next, or choose an owner |
| `/` | filter by name or branch |
| `f` / `F` | fetch the clone / every clone |
| `p` | move the clone's default branch forward |
| `P` | fetch every clone, then move each default branch forward where nothing is in the way (asks first) |
| `d` | clean up the clone (below; asks first) |
| `D` | clean up every clone's finished worktrees (asks first) |
| `c` | clone the repository selected |
| `r` | read the clones and GitHub again, all of them |
| `L` | read the list of an owner that is not yours |
| `o` / `y` / `Y` / `R` | open on GitHub / copy the path / copy the URL / reveal in Finder |
| `esc` | put away the results shown on rows |
| `w` | show the workers in use while gh tidy works |
| `?` | every key |

Every key can be changed under `[keys]`. The mouse selects, opens and folds; it never changes anything. [docs/keys.md](docs/keys.md) lists every key with its name under `[keys]`, what the mouse does, and the marks.

### Moving the default branch forward

`p` and `P` move the default branch, not the branch checked out: work in progress is not touched. Each clone is fetched first, and then:

- where the default branch is checked out nowhere, it is moved with `git fetch . origin/<default>:<default>`, which only fast-forwards;
- where it is checked out, it is merged with `git merge --ff-only`, only when that worktree has no changes to tracked files.

A clone is skipped, with why, when it has no local default branch, when its default branch has commits `origin` lacks, tracks something other than `origin/<default>`, is checked out in more than one worktree or in one with a rebase or bisect stopped in it, or when its name is only a guess (run `git remote set-head origin -a`).

### Cleaning up

`d` measures the selected clone against `origin/<default>` and lists what it would remove and what it keeps, with why; nothing is removed until you confirm. Each clone is read again before anything goes, and each worktree right before it is removed, so what changed since the confirm is left.

- A worktree goes when its branch is merged, it has no changes, and it was committed to after it was made. A worktree never committed to is kept: its branch sits on the default branch, so it looks merged, but it may be the one you are about to work in. So is the worktree gh tidy runs in.
- A worktree whose directory is gone is pruned.
- A merged branch checked out nowhere goes.
- A branch whose upstream was deleted but which is not merged, as after a squash merge, is kept unless `delete_gone = true`. `D` never deletes it.

## Settings

`~/.config/gh-tidy/config.toml`. Every setting is optional; `gh tidy --print-config` prints them all at their defaults, and [docs/configuration.md](docs/configuration.md) shows the same. A mistake is reported with where it is in the file.

You can choose the columns of each set and the parts of the pane, gather repositories into sections of your own (`[[sections]]`), change the marks to match your own prompt, add or hide owners, and change the keys.

### Handing work to other tools

gh tidy runs git and gh, and nothing else on its own. Two settings let it hand work to other tools.

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
key = "T"
name = "trash"
confirm = true
run = ["gomi", "{path}"]
```

`worktree_remove` is how `d` removes a worktree, for worktrees another tool made and should forget too. The command runs in the clone and must remove the worktree at `{path}`:

```toml
[cleanup]
worktree_remove = ["remove-worktree", "{path}"]
```

`d` still decides what goes; the command only removes it.

## License

MIT
