// Package ledger defines the decision ledger record: one immutable row per
// decision (rule, classifier, model, human, reflex, policy), plus an
// append-only event log for what happens next (disposition changes, human
// corrections, outcomes).
//
// # Rows and events
//
// A Record is written once. Its IdempotencyKey, from Key, is one per subject,
// stage and version, so a retry or a replay appends nothing and a changed rule
// set or model appends a new row. Events are numbered per decision. A
// disposition moves one way: a suggestion resolves any way, and every other
// state can only be superseded. A correction needs a label and an actor, and
// Entry.Label returns the latest correction in preference to the original
// output, so human corrections are first-class training labels that never
// rewrite history.
//
// # Retention
//
// Metadata, hashes, labels, corrections and outcomes are kept indefinitely.
// Only the raw input is governed. Retention.Shape applies per-stage rules when
// a record is built: snapshot, pointer or hash only; a time to live;
// sampling; a redaction function (a failing redactor refuses the write rather
// than storing raw input); a Keep flag that exempts a training row; and an
// optional HMAC key for the input hash. A Store that implements Expirer drops
// expired snapshots and keeps everything else.
//
// # Storage
//
// The package is storage-agnostic. Store is the interface a service
// implements; MemStore is an in-memory implementation; package ledgertest is
// the conformance suite every implementation must pass. Standard library only.
package ledger
