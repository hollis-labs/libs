// Package retention is the configuration and decision model for tiered event
// retention (design D7, revision 2). It holds no storage code: a durable
// [streamhub.Log] or its janitor asks it which class an event belongs to and
// which tier an event of a given age is in, and it validates operator
// configuration.
//
// Every duration is configuration per retention class; none is hard-coded in
// a producer or a log. [Defaults] returns the proposed tiers. A duration of
// zero means "keep forever".
//
// An event moves through tiers by age:
//
//	age < Full                 TierFull      whole row, payload included
//	Full <= age < Envelope     TierEnvelope  payload tombstoned, envelope kept
//	age >= Envelope            TierTrimmed   eligible for head trim, once rolled up
//
// Rollup buckets (counts per bucket over low-cardinality dimensions) are kept
// on their own schedule; see [Rollup].
//
// Two rules are not configuration. Secret payloads are never stored, so a
// config that asks to store them is rejected. Lengthening the PII payload cap
// past [DefaultPIIPayloadMax], or setting the audit class's Full below
// [AuditFullFloor], needs an explicit Acknowledge in the config and produces
// a warning.
//
// Changes apply forward only: this package never restores data that was
// already tombstoned or trimmed.
package retention
