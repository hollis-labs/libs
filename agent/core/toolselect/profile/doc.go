// Package profile is a pure evaluator that decides which tools of an MCP
// server catalog a caller sees.
//
// [Evaluate] takes a [Catalog] and a [Profile] and returns the visible tools,
// in a fixed order, and a [HiddenReason] for every tool that is not visible.
// It performs no I/O, reads no clock and keeps no state, so the same inputs
// always give the same outputs.
//
// The zero Profile is meaningful: every tool of every server is visible and
// nothing is hidden.
//
// # Precedence
//
// A tool is checked against, in order: its server being disabled, the deny
// globs, the read-only filter, and the allow globs. Deny wins over read-only,
// which wins over allow. The first check that hides a tool names the
// [HiddenCause].
//
// # Annotations
//
// ReadOnly and Destructive are MCP annotation hints. They are passed through
// verbatim: nil means the upstream did not declare the hint and stays nil. A
// Profile with ReadOnly set hides a tool whose hint is not exactly true, so an
// undeclared hint is hidden rather than assumed safe. This is visibility
// only; it is not authorization, and annotations are untrusted.
//
// # Validation
//
// A server id in Profile.Servers that the catalog does not contain is
// [ErrUnknownServer]. A malformed glob in ToolsAllow or ToolsDeny is
// [ErrBadPattern]. A well-formed glob that matches no tool is a no-op.
// Order and AlwaysLoad list exact tool names, not globs.
package profile
