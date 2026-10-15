// Package ghmerged implements worktree.MergedSource on top of the GitHub CLI.
//
// It runs `gh pr list --state merged --json headRefName,headRefOid` in the
// repository directory and is the only place in this module that executes
// `gh`. Unlike Torque's original helper it does not swallow failures: a missing
// or unauthenticated `gh` returns an error (a missing binary wraps
// [ErrUnavailable]), so a sweep reports it instead of silently reaping nothing.
package ghmerged
