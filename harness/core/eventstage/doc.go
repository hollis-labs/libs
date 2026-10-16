// Package eventstage is the ordered stage chain every event passes through at
// a producer's emit choke point (event stream contract section 8, D4 rev 2):
//
//  1. Stamp    fills scope, corr and actor from verified context. Supplied by
//     each producer. It only omits what it cannot verify, never guesses.
//  2. Sanitize removes secrets and PII from the payload and sets class.sanitized,
//     class.redactions, class.sensitivity and class.pii. It fails closed.
//  3. Classify attaches class.category, class.severity and class.labels from a
//     versioned rule set. It runs only after Sanitize, so it never sees a value
//     Sanitize would have removed.
//  4. Persist and fan out are the producer's own and follow the chain.
//
// React (reaction rules) is out of scope for this arc.
//
// A [Stage] mutates the event in place and reports what it did as an
// [Outcome]. [Chain] fixes the order, contains panics and enforces the one
// safety property: Classify does not run unless Sanitize completed, either
// normally or by failing closed. The chain is generic over the envelope; this
// package supplies Sanitize adapters and a Classify stage for both
// [runtimeevents.Event] and [mesh.Event].
//
// Sanitize calls a [PayloadSanitizer], the payload API of the shared redaction
// engine (security/redact, D5, task CW-20261016-0144). [NewPayloadSanitizer]
// supplies the tagged engine adapter, selecting explicit exact/family policies
// before catalog defaults. [FailClosedClasses] is the shared, closed vocabulary
// of error payloads; unknown failure labels become "error".
//
// # Classify rules
//
// Rules are data: a JSON document with schema "eventstage.rules/v1" that
// carries its own tests. See [RuleSet] and [ParseRules].
package eventstage
