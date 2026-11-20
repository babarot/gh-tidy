# ADR-008: Clones live under roots the settings name, never ones guessed

## Status

Accepted

## Context

gh tidy looked for clones under one root: `[core] root`, else the first `ghq.root` in git config, else `~/ghq`. People keep clones in more than one place (their own and their employer's), and reading `ghq.root` tied gh tidy to one tool for managing clones; git itself has no setting for where clones live, and every such tool keeps its own key.

When no root is set, the root of the clone gh tidy starts in can be worked out from the clone's path and its origin. Running on that root, though, would make the same command show other clones depending on where it starts, hide every other root without saying so, change where `c` clones, and replace each owner's cache with every switch.

## Decision

- Clones are read under the roots `[core] roots` names, in order, each laid out as `<root>/<host>/<owner>/<repo>`. No tool's settings are read for them. Every root is read alike: the owners offered, the owner opened from where gh tidy starts, an owner's clones, all, and `--json`.
- With no roots set, gh tidy never runs on a root it worked out. It offers the root of the clone it starts in, and writes it only when the user says yes on a terminal, to a config file that does not exist yet; it does not edit a file the user wrote. Otherwise it stops with the line to write.
- A clone is one row whichever way it is reached: roots are resolved and taken once, a root inside another is a mistake, and an owner's directory two roots reach through a symlink is read once, from the first root. A clone's path stays under the root it was found from.
- What is missing is forgotten only once every root was read, with the clones of all of them; a root that cannot be read keeps what is known of its clones. A clone kept from a root no longer among the roots is not shown or acted on; a repository of the owner's list stays as not cloned.
- Rejected:
  - Reading `ghq.root` when no root is set: it ties gh tidy to ghq, and reading one tool's key would invite reading every other tool's.
  - Running on the root worked out from where gh tidy starts, without saving it: the screen would change with the place started from, other roots would be hidden, and the cache would be read again from GitHub at every switch.
  - Another layout under a root (clones directly in it, or found by searching): matching rows to GitHub needs a host and an owner for every clone, and a search would cost a walk of the disk at every read.

## Consequences

- A new place that uses the roots reads every root, and forgets only after all were read; the engine's tests for several roots are in [internal/engine/roots_test.go](../../internal/engine/roots_test.go).
- Where `c` clones is decided by gh tidy, without asking; a way to choose is left for later.
- A config file setting `[core] root` is told to write `roots` instead.
