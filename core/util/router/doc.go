// Package router is a small declarative matcher with a real confidence gate, a
// policy hook and an escalation hook. It maps the fields of a Subject to a
// target, and returns a Decision shaped to be one row of a decision ledger.
//
// # Rules
//
// A RuleSet has a version, an explicit Strategy (FirstMatch or FanOut; there is
// no default) and rules. A rule is a list of conditions on flat dotted field
// names (eq, not_eq, in, prefix, glob, exists), a target, a priority, and
// optionally a minimum confidence. Compile validates the set and fingerprints
// its content, so a decision records exactly which rules produced it.
//
// # Disposition
//
// A matching rule yields Apply, Ask, Deny or Abstain. A rule that is not Auto
// only suggests (Ask). A rule with MinConfidence applies the gate: a confidence
// below it, or unknown, is not zero and not a pass; the rule's OnLowConfidence
// decides (default Ask).
//
// # Policy and escalation
//
// If Config.Policy is set, each candidate match is evaluated and the most
// restrictive of the rule and the policy wins: a verdict can tighten a match,
// never loosen it. Obligations are carried on the match. Approval turns it into
// Ask, dry-run is flagged for the consumer, a rate limit travels with its
// limit, and an unknown kind denies. An error from the policy denies. With
// Config.PolicyShadow the verdicts are recorded and not applied. When nothing
// routed, Config.Escalator can hand the subject to a later stage.
//
// # Decision
//
// Decision keeps the input snapshot (shaped by Config.Snapshot, which is where
// redaction belongs), the rule-set version and hash, every rule considered and
// why it did or did not match, and any shadow verdicts. Decision.LedgerRecord
// converts it to a ledger row.
//
// The package is standard library plus the ledger package, and it does not
// import the policy module (core may not import security); an adapter in that
// module converts.
package router
