# ADR-010: The demo runs gh tidy against a made-up world, outside it

## Status

Accepted

## Context

The README shows gh tidy at work in a recording. A recording made against a real account changes every time it is made, and shows real names: the user's repositories, the people who opened their issues, the packages they depend on. gh tidy reads GitHub and the clones on disk through interfaces the engine defines, which the tests already fill with made-up ones.

## Decision

- The recording runs gh tidy's real engine and screen, with the real parts that read and change clones, against a made-up world: real git clones and remotes in a temporary directory, and a made-up GitHub that answers from them. The world is in [demo/internal/recording](../../demo/internal/recording), the program in [demo/recording](../../demo/recording), with the tape VHS records.
- The demo is never built into gh tidy. gh tidy has no flag, variable or setting that points it at made-up data, so nothing a user runs shows made-up data or skips GitHub. [internal/app](../../internal/app) hands the demo the real parts, so that the demo runs what gh tidy runs rather than a copy of it.
- The world is made up: no real organization, repository, person or package. Everything the demo reads and writes (the config file, the cache, the state, what was seen) is under its temporary directory, and git there reads none of the user's settings and reaches nothing but the disk.
- The recording is kept in git, where the README shows it, and made again with the tape when the screen changes; it is small enough that keeping each version costs little.
- Rejected:
  - A flag in gh tidy that switches to made-up data: a way into the shipped binary that a user could reach.
  - A made-up GitHub over HTTP with gh tidy pointed at it: gh tidy would need a way to change its host, and answers to every query it makes.
  - Recording against a GitHub account kept for the demo: the recording would change each time, and real names would show.

## Consequences

- The demo builds with `go test ./...`: an interface the made-up world fills that gains a method fails the build, and a test runs the world through the real engine.
- A change to what the screen shows may need the world's data or the tape changed, and the recording made again.
