// Package conformance holds the fixtures that pin the reflex definition
// contract and a reference runner for them.
//
// A case is a directory testdata/<valid|invalid>/<name>/ with two files:
//
//	input.json  a Definition, in the JSON shape of reflexspec.Definition
//	want.json   {"valid": bool, "errors": [{"field","code"}], "reason": "...",
//	            "options": {"known_action_kinds": [...], ...}}
//
// An invalid case records the exact (field, code) pairs the validator must
// report, in any order, and a human reason. options, when present, injects
// the named catalogs the way reflexspec.WithKnownActionKinds and friends do.
//
// The fixtures are Go-only for now: no other-language runner exists, and none
// is claimed. A port passes when it reports the same (field, code) pairs.
package conformance
