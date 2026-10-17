// Package routepolicy adapts a policy.Evaluator to the router's Policy seam
// (core/util/router). It is its own module: core may not import security, and
// security/policy stays a neutral contract with no adapters and no dependencies
// on other modules.
//
// The evaluator already owns enforce and shadow modes, fail-closed and
// fail-open-with-audit, and the audit event, so the adapter adds none of that.
// A Mapper projects a routing candidate to a policy.Request; the adapter runs
// the evaluator and translates the Result back. The verdict effect is the
// evaluator's effective admission, obligations and policy provenance come
// through unchanged, and a failure is a fixed classification such as pdp_failed,
// never error text, which can carry private input.
//
// DefaultMapper builds a request from a Subject's principal fields, with the
// action "route:<target>" and the target as the resource. Hosts that route
// untrusted subjects should supply their own Mapper and decide which fields
// reach the policy decision point.
package routepolicy
