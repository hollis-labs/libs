package render

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
)

var ErrUnsafePath = errors.New("render: unsafe relative path")

// ValidateRelPath is the canonical render destination rule. It builds on the
// artifact contract and reserves sensitive root names, unresolved placeholders,
// drive prefixes and control characters. Legacy callers migrate separately.
func ValidateRelPath(rel string) error {
	if !utf8.ValidString(rel) || len(rel) > MaxPathBytes || artifact.ValidateRelPath(rel) != nil || strings.ContainsAny(rel, ":{}\x00\r\n") {
		return ErrUnsafePath
	}
	parts := strings.Split(rel, "/")
	if len(parts) > MaxTreeDepth {
		return ErrUnsafePath
	}
	for _, part := range parts {
		if len(part) > MaxComponentBytes {
			return ErrUnsafePath
		}
		for _, r := range part {
			if unsafePathRune(r) {
				return ErrUnsafePath
			}
		}
	}
	for _, name := range []string{".git", ".ssh", ".gnupg", ".aws", ".materialize"} {
		if strings.EqualFold(parts[0], name) {
			return ErrUnsafePath
		}
	}
	return nil
}

// ValidateComponent uses the same single-component substitution rule as plan.
func ValidateComponent(component string) error {
	if len(component) > MaxComponentBytes {
		return ErrUnsafePath
	}
	if _, err := plan.Expand("{name}", map[string]string{"name": component}); err != nil {
		return ErrUnsafePath
	}
	return nil
}

// Render bounds are byte lengths and count explicit plus synthesized entries.
// They bound filesystem destinations and the quadratic artifact normalizer.
const (
	MaxComponentBytes = 255
	MaxPathBytes      = 4096
	MaxTreeDepth      = 64
	MaxTreeEntries    = 4096
)

func unsafePathRune(r rune) bool {
	return r < 32 || r == 127 || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}
func safeRootText(value string) bool {
	for _, r := range value {
		if unsafePathRune(r) {
			return false
		}
	}
	return true
}
