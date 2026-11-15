// Package github reads GitHub through gh's own authentication: an owner's
// list of repositories by REST, with conditional requests, and the fields
// of repositories asked for by name by GraphQL, batched with aliases. Asking
// for each repository's default branch while listing an owner's
// repositories is slow (half a minute for 400), so the two are separate
// (ADR-004).
package github

import "errors"

// ErrNoOwner is returned when no user or organization has the login.
var ErrNoOwner = errors.New("no such user or organization on GitHub")
