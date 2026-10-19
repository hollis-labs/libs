// Package reflexspec is a portable contract for one reflex definition: the
// JSON record shape, the trigger_spec predicate/event/interval grammar and
// the per-kind action_spec, with a validator that checks all three.
//
// A reflex is a steering rule: a trigger evaluated over a snapshot of an
// agent session, and an action to stage or apply when it fires. The engine
// that evaluates triggers is go-reflexes; this package does not import it and
// runs nothing. It answers only "is this definition well-formed?", including
// the one question an engine cannot answer for its author: a predicate node
// whose kind is misspelled is, to the engine, an error that its caller
// swallows, so the reflex silently never fires. Validate, ValidateTriggerSpec
// and ValidateActionSpec turn that into an error at authoring time.
//
// The predicate grammar (PredicateKinds) is that of go-reflexes v0.1.0. It is
// encoded here as data; TestGoReflexesVocabularyDrift compares it with a local
// go-reflexes checkout when one is present. Enums a host owns (action kinds,
// statuses, provenance tiers, event names) are open: shape-checked by default,
// and checked against a catalog only when the caller injects one with
// WithKnownActionKinds and friends. Provenance-tier authorization is host
// policy and is not checked here.
//
// Every failure is an *Error with a field path and a stable Code. The
// conformance sub-package carries the fixtures that pin the behavior.
package reflexspec
