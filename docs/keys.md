# Keys

Every key gh tidy reads, with its action's name under `[keys]` in the config file ([configuration](configuration.md)), what the mouse does, and the marks. `?` shows the same in gh tidy, with the keys you set.

This page is written from the code (`go test ./internal/tui -run TestKeysReference -update`).

## Anywhere

| Key | Action | Does |
|---|---|---|
| `q` | `quit` | quits, once the changes running end |
| `?` | `help` | shows every key |
| `esc` |  | steps back one level everywhere |
| `ctrl+c` |  | quits from anywhere |

## Moving, in the list and in a popup

| Key | Action | Does |
|---|---|---|
| `up` `k` `ctrl+p` | `up` | moves the cursor up |
| `down` `j` `ctrl+n` | `down` | moves the cursor down |
| `home` `g` | `top` | moves to the first row |
| `end` `G` | `bottom` | moves to the last row |
| `pgup` `ctrl+b` | `page_up` | moves a screen up |
| `pgdown` `ctrl+f` | `page_down` | moves a screen down |

## The list

| Key | Action | Does |
|---|---|---|
| `enter` | `expand` | shows a clone's worktrees, or folds a section |
| `v` | `lens` | switches between the local and the github columns |
| `<` | `prev_owner` | shows the previous owner |
| `>` | `next_owner` | shows the next owner |
| `O` | `choose_owner` | chooses an owner from a list |
| `/` | `search` | filters the rows by name or branch |
| `f` | `fetch` | fetches the clone |
| `F` | `fetch_all` | fetches every clone |
| `p` | `pull` | moves the clone's default branch forward |
| `P` | `pull_all` | fetches every clone, then moves each default branch forward; asks first |
| `d` | `cleanup` | cleans up the clone; asks first |
| `D` | `cleanup_all` | cleans up every clone's finished worktrees; asks first |
| `c` | `clone` | clones the repository selected |
| `r` | `refresh` | reads the clones and GitHub again, all of them |
| `L` | `list` | reads the list of an owner that is not yours |
| `w` | `workers` | shows how many of each kind of work run, while gh tidy works |
| `o` | `open_browser` | opens the repository on GitHub |
| `R` | `reveal` | shows the clone in Finder |
| `y` | `copy_path` | copies the path |
| `Y` | `copy_url` | copies the URL |
| `+` | `preview_grow` | makes the pane taller |
| `-` | `preview_shrink` | makes the pane shorter |
| `J` | `preview_down` | scrolls the pane down |
| `K` | `preview_up` | scrolls the pane up |

## A popup with a list

| Key | Action | Does |
|---|---|---|
| `enter` | `choose` | chooses the item |
| `type` |  | narrows the list |

## A popup that asks before a change

| Key | Action | Does |
|---|---|---|
| `y` `enter` | `confirm` | runs the change asked about |

## Typing the filter

| Key | Action | Does |
|---|---|---|
| `enter` | `accept` | keeps the filter |
| `backspace` | `erase` | erases a character |

## The mouse, which only moves, opens and folds

| | Does |
|---|---|
| click | puts the cursor on a row |
| double click | shows a clone's worktrees, or folds a section |
| wheel | over the list moves the cursor; over the pane scrolls it |
| click the owner | chooses an owner; ‹ and › step to the previous and next |
| click local, github | switches the columns |
| click a pane's head | folds that part of the pane |
| drag the pane's top | makes the pane taller or shorter, as + and - do |
| click a key hint | presses the key, unless it changes a clone or asks GitHub |
| click in a popup | puts the cursor on an item; a click on it again chooses it |
| click outside | closes the popup |

## Marks, set under [symbols]

| | Does |
|---|---|
| `<` | behind: commits to bring in |
| `>` | ahead: commits not pushed |
| `<>` | behind and ahead; with counts, both numbers (<2>1) |
| `=` | even with what it follows |
| `⇣` | a branch behind the default branch |
| `?` | GitHub has commits not fetched yet |
| `*` | files changed, not staged |
| `+` | files staged |
| `%` | files not tracked |
| `$` | stash entries |
| `⌥` | worktrees besides the main one |
| `⋅` | not read yet |
