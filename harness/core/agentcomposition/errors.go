package agentcomposition

import "errors"

var (
	// ErrMissingCompositionDefinition is returned when an authored
	// recipe references a required base or part that is not supplied in
	// ComposeRequest.Definitions.
	ErrMissingCompositionDefinition = errors.New("agentcontext: missing composition definition")

	// ErrCompositionCycle is returned when recipe base/part references form
	// a cycle.
	ErrCompositionCycle = errors.New("agentcontext: composition cycle")

	// ErrCompositionConflict is returned when deterministic merge rules
	// cannot reconcile duplicate or opaque authored fields.
	ErrCompositionConflict = errors.New("agentcontext: composition merge conflict")
)
