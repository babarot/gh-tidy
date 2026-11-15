# ADR-004: GitHub is read with conditional lists and small batches, and a refusal is never zero

## Status

Accepted

## Context

gh-tidy lists an owner's repositories (405 for babarot, possibly hundreds for an organization) and reads fields of many of them, with a token whose hourly points are shared with gh-desk and gh. Measured on 2026-11-14 and 15:

- Listing with GraphQL is slow once `defaultBranchRef` is asked for at all: 6 seconds for 405 repositories became 25. With every heavy field on a page of 100, GitHub cut the query off at its 10-second limit (502), and it takes extra points for the next hour after a cut-off.
- The REST list returns the default branch's name with no such cost, and answers a conditional request with 304 without counting it against the rate limit; 5 pages answered 304 in 3 seconds at a cost of 0.
- Heavy fields asked for by name, ten repositories a query, took 2 to 4 seconds with every field gh-tidy reads, at 1 point a query.
- A token not authorized for an organization's SSO is refused (403 with `X-GitHub-SSO`), or given part of the list; another user's list holds public repositories only. A repository the token may not read answers NOT_FOUND, like one that does not exist.

## Decision

- An owner's list is read through REST: the viewer's own repositories, an organization's, or another user's, by who the owner is to the viewer. A list read before is asked for again page by page with its ETags; when every page answers 304 the list has not changed. When any page has changed, every page is read again, so a list is never put together from pages read at different times.
- A list says how complete it is: complete, partial (with why: SSO, another user's public repositories, or more pages than gh-tidy reads unasked) or unreadable (with why). A repository missing from a list that is not complete may exist.
- Fields that cost GitHub time are read through GraphQL by name, a few repositories a query, and only the fields the screen needs. The number a query holds is a constant, small enough to stay well under GitHub's time limit; a cut-off is reported as such, so the caller asks for fewer.
- A value GitHub refuses is never shown as zero: a repository not found, Dependabot alerts turned off, and alerts the token may not read are each told apart from a count.
- Every response's rate limit is returned to the caller, and a refusal for a rate limit says whether the hourly points ran out (wait for the reset) or a secondary limit was hit (wait as GitHub says, or a minute). What to hold back because of it is decided above this package.
- [internal/github](../../internal/github) is the only package that uses `net/http`. It reads GitHub through gh's own authentication and never writes to GitHub.
- Tests run against a made-up GitHub put behind the client's transport; gh-tidy has no flag or variable that points it elsewhere. `TIDY_LIVE=1` runs the tests that ask GitHub for real.
- Rejected: listing with GraphQL, for the cost of `defaultBranchRef` above, and since GraphQL has no conditional requests.
- Rejected: asking each repository with its own REST request and ETag. Hundreds of requests would be sent one after another, as GitHub asks, and take minutes.

## Consequences

- An owner's list is cheap to read again when nothing changed, but an active organization's list changes often and costs its pages each time.
- A field added to what gh-tidy reads by name must be timed with a full batch against GitHub's time limit before it is added.
- Code that shows a GitHub value must handle not found, off and not readable besides a value.
