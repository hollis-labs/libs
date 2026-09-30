// Package hitl is a kind-agnostic contract for asking a human (or any slow
// responder) and getting back exactly one immutable outcome: a handle to await
// or withdraw, then one of five terminal results.
//
// The package has three layers, all stdlib-only:
//
//   - Wire types (this file's siblings): State and CanTransition, Handle, the
//     Get/Await/Withdraw commands, RetrievalResult, the sealed Outcome union
//     (Resolved, Canceled, Expired, Failed, Superseded) and the typed errors
//     with DecodeError/EncodeError. Commands decode strictly, outputs
//     tolerantly.
//   - Store and Record: the persistence boundary, with CheckSwap holding the
//     compare-and-set invariants every Store must apply.
//   - Service: the reference implementation over a Store. It provides an
//     idempotent Enqueue, Get, Await, Withdraw, Respond and ExpireDue with
//     first-terminal-wins and store-owner-enforced expiry.
//
// The JSON Schema bundle lives in package schema, an in-memory Store in
// memstore, and the conformance kit in hitltest. See docs/CONTRACT.md for the
// normative prose.
//
// # Identity of the responder
//
// A Participant names who answered as an open responder{kind, ref}, carries an
// open-string assurance and an optional Proof{scheme, key_ref, binds} for
// credentials such as a WebAuthn assertion. Core carries a proof; it does not
// verify one and does not rank it against assurance. That is caller policy.
//
// # Expiry
//
// expires_at is enforced by whoever owns the record, not by the transport: a
// respond or withdraw arriving at or after expires_at is refused atomically,
// even if no sweeper has ever run.
package hitl
