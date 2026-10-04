package render

import (
	"errors"
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

var ErrUnsafePath = errors.New("render: unsafe relative path")

// ValidateRelPath is the canonical render destination rule. It builds on the
// artifact contract and reserves sensitive root names, unresolved placeholders,
// drive prefixes and control characters. Legacy callers migrate separately.
func ValidateRelPath(rel string) error {
	if artifact.ValidateRelPath(rel) != nil || strings.ContainsAny(rel, ":{}\x00\r\n") {
		return ErrUnsafePath
	}
	first, _, _ := strings.Cut(rel, "/")
	switch first {
	case ".git", ".ssh", ".gnupg", ".aws", ".materialize":
		return ErrUnsafePath
	}
	return nil
}

// ValidateComponent uses the same single-component substitution rule as plan.
func ValidateComponent(component string) error {
	if _, err := plan.Expand("{name}", map[string]string{"name": component}); err != nil {
		return ErrUnsafePath
	}
	return nil
}
