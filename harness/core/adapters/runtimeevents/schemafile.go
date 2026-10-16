package runtimeevents

import _ "embed"

// JSONSchema is the JSON Schema (draft 2020-12) for the [Event] envelope,
// schema_version "1" with the v1.1 blocks. It mirrors [Event.Validate] and,
// like it, accepts unknown kinds, fields and vocabulary values. Conformance
// fixtures for other languages are in testdata/conformance.
//
//go:embed schema/event.v1.schema.json
var JSONSchema []byte
