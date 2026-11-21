# Configuration

gh tidy reads `~/.config/gh-tidy/config.toml` (or `$XDG_CONFIG_HOME/gh-tidy/config.toml`). Every setting is optional: one left out takes its default. A mistake is reported with where it is in the file, and a setting that is no longer read says why.

This page is written from the code (`go test ./internal/config -run TestConfigurationDoc -update`). `gh tidy --print-config` prints the same, to start a file from.

```toml
# gh tidy settings. Uncomment a line to change it.

[core]
# Where clones live, each laid out as <host>/<owner>/<repo>, such as ["~/src",
# "~/work"]. c clones into the first that holds the owner's directory, else
# the first. Empty (default): gh tidy offers the root of the clone it starts
# in, and writes it here when you say yes.
# roots = []

[owners]
# The owners offered are the viewer, the viewer's organizations and the owners
# with clones under root.
# Offer these owners too, by login.
# add = []
# Leave these owners out, written the same way.
# hide = []

[tui]
# Color scheme: "auto" (default) picks catppuccin-mocha on a dark terminal and
# catppuccin-latte on a light one. Also: tokyo-night, dracula, nord,
# gruvbox-dark, and ansi (the terminal's own 16 colors).
# theme = "auto"

[columns]
# The columns of each set, in order. v switches between the sets.
# The local set, of: repo, main, checked_out, changes, worktrees, fetched.
# local = ["repo", "main", "checked_out", "changes", "worktrees", "fetched"]
# The github set, of: repo, issues, prs, dependabot, ci, stars, pushed,
# visibility, main.
# github = ["repo", "issues", "prs", "dependabot", "ci", "stars", "pushed", "visibility", "main"]

[preview]
# The parts of the pane below the list, in order: incoming (commits on
# origin's default branch that p brings in), unpushed, behind_main (commits on
# the default branch the checked out branch lacks), changes (the files
# changed), stash and worktrees. A part with nothing to show is left out.
# sections = ["incoming", "unpushed", "behind_main", "changes", "stash", "worktrees"]

[symbols]
# The marks the list writes, after zsh-mini-prompt. Change them to match your
# prompt; each must be one cell wide per character, and an empty one writes
# nothing.
# Files changed, not staged.
# unstaged = "*"
# Files staged.
# staged = "+"
# Files not tracked.
# untracked = "%"
# Stash entries.
# stash = "$"
# Behind: commits to bring in.
# behind = "<"
# Ahead: commits not pushed.
# ahead = ">"
# Behind and ahead, when counts are not written.
# diverged = "<>"
# Even with what it follows.
# synced = "="
# A branch behind the default branch.
# behind_main = "⇣"
# GitHub has commits not fetched yet.
# github_ahead = "?"
# Worktrees besides the main one.
# worktree = "⌥"
# Write how many after a mark (*3); false writes the mark alone (*).
# counts = true

[cleanup]
# Branches d never deletes, besides the default branch and the current one.
# protect = []
# Also delete branches whose upstream is gone but which are not merged (a
# squash merge), with git branch -D.
# delete_gone = false
# How d removes a worktree it decided to remove, as a command and its
# arguments, run in the repository. It must pass {path}. For worktrees made by
# another tool, its own command can do it, so the tool forgets it too.
# worktree_remove = ["git", "worktree", "remove", "{path}"]

[keys]
# Which keys do what, by action: a key or a list of keys, replacing the
# action's own, or [] to turn it off. ctrl+c and esc are fixed.
# quit = "q"
# help = "?"
# up = ["up", "k", "ctrl+p"]
# down = ["down", "j", "ctrl+n"]
# top = ["home", "g"]
# bottom = ["end", "G"]
# page_up = ["pgup", "ctrl+b"]
# page_down = ["pgdown", "ctrl+f"]
# expand = "enter"
# lens = "v"
# prev_owner = "<"
# next_owner = ">"
# choose_owner = "O"
# search = "/"
# fetch = "f"
# fetch_all = "F"
# pull = "p"
# pull_all = "P"
# cleanup = "d"
# cleanup_all = "D"
# clone = "c"
# refresh = "r"
# list = "L"
# workers = "w"
# open_browser = "o"
# reveal = "R"
# copy_path = "y"
# copy_url = "Y"
# preview_grow = "+"
# preview_shrink = "-"
# preview_down = "J"
# preview_up = "K"
# choose = "enter"
# confirm = ["y", "enter"]
# accept = "enter"
# erase = "backspace"

# Sections of the list you name: every row whose repository matches one of
# repos goes in the first section it matches, whatever it needs, in both
# sets of columns. A pattern with a slash is matched against owner/name, one
# without against the name, in any case. A clone is matched by where it
# lies (<owner>/<directory>, so a clone of other/x under acme/ is acme/x);
# a repository not cloned by its name on GitHub.
#
# [[sections]]
# name = "experiments"
# repos = ["play-*", "scratch*", "acme/sandbox-*"]
# folded = true        # start folded

# Custom commands, run on the selected repository or worktree. run is the
# command and its arguments; these are replaced in each argument:
# {path}, {repo} (owner/name), {owner}, {name}, {branch}, {default}, {url}.
#
# [[actions]]
# key = "H"
# name = "open in herdr"
# scope = "worktree"   # "repo" (default) or "worktree"
# run = ["herdr", "worktree", "open", "--path", "{path}"]
# interactive = false  # true hands the terminal to the command
# confirm = false      # true asks before running it
```
