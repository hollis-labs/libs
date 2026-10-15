// Package summary renders a human-readable Markdown description of a
// session's effective path access (allow-list, session grants, inherited
// grants and deny rules) for inclusion in an agent's prompt.
//
// See RenderPermissionSummary. The renderer is a pure, deterministic
// projection of its input: it does no enforcement and reads no state, and it
// takes grants as plain string slices so it does not depend on the pathgrants
// package. Resolve workspace-relative rule patterns with RuleSet.Resolve
// before rendering.
package summary
