# ADR-003: Changes to a clone are decided again right before they are made, and never forced

## Status

Accepted

## Context

gh-tidy changes the clones a user and their coding agents work in: it moves default branches forward and removes worktrees and branches, often across many clones at once. Between the moment a user confirms such a change and the moment it runs, an agent may start working in a worktree, or a branch may move. The prototype checked a worktree for changes when it made its plan, and a branch for being merged right before deleting it, but nothing in between.

The prototype also pulled the branch checked out in each clone. The problem gh-tidy solves is a stale default branch, which a clone working on a feature branch keeps however often that branch is pulled.

## Decision

- Deciding and reading are apart. [internal/local](../../internal/local) reads facts from git; [internal/policy](../../internal/policy) decides from those facts alone and reaches nothing outside the process, so every rule below is tested there without git.
- A change is decided again from facts read right before it is made, in the clone's lane, not from what was shown or confirmed. Of what a confirmed plan removes, only what a plan made again still removes is carried out; what changed since is left alone and said so. A worktree's changes are read once more right before it is removed, and a branch is checked once more for being merged into the base right before it is deleted.
- Moving many clones' worktrees at once takes the same path as one clone, clone by clone, each decided again when its turn comes. It never deletes a branch that is not merged, whatever the settings say for one clone.
- A default branch is moved forward only by a fast-forward to `origin/<default>`:
  - only when its name came from GitHub or origin/HEAD, never from a guess;
  - only when the local branch exists (moving it never creates one) and tracks `origin/<default>`;
  - where it is checked out nowhere, by updating the ref with a refspec that cannot force;
  - where it is checked out, by a fast-forward merge in that worktree, only when the worktree has no changes and no operation stopped half way, and never by a bare pull, which follows the branch's own upstream.
  - Which worktree holds the branch is read by gh-tidy itself right before; git's own refusal is not relied on, since versions differ.
  - The branch being worked on is never touched.
- d keeps what holds the user's work unless a setting says otherwise: a worktree with changes, one never committed to, a locked one, one with an operation in progress, the one gh-tidy runs in, a protected branch, and a branch not merged into the base.
- Rejected: pulling the checked-out branch, as the prototype did. It leaves the default branch stale in every clone working on another branch.
- Rejected: checking once when the plan is made. The time between a confirm and the last removal of a long batch is where an agent starts working.

## Consequences

- Every new kind of change to a clone needs its facts in internal/model, its rule in internal/policy with tests, and a reading of the facts right before it runs.
- A clone whose default branch tracks another remote, or whose default branch name is only guessed, is never moved forward; the user sees why.
- A confirmed batch may do less than it showed, and says what it left and why.
