package bootkey

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// Version identifies the encoding in every returned, persisted key.
	Version = "v1"
	// MaxSlugLength bounds the cosmetic ASCII prefix, excluding version/hash.
	MaxSlugLength = 48
	// MaxKeyLength is the maximum encoded component length in bytes.
	MaxKeyLength = len(Version) + 1 + MaxSlugLength + 1 + sha256.Size*2
)

// ErrInvalidComponent reports a component that is unsafe to join onto a root.
var ErrInvalidComponent = errors.New("bootkey: invalid path component")

// ErrEmptyIdentity reports missing identity input. Encode does not infer one.
var ErrEmptyIdentity = errors.New("bootkey: identity is required")

// Encode returns a versioned, readable, collision-resistant key. Every input,
// including a string that already looks like an encoded key, receives its own
// full digest. No trimming, case folding or path cleaning changes digest input.
func Encode(identity string) (string, error) {
	if identity == "" {
		return "", ErrEmptyIdentity
	}
	sum := sha256.Sum256([]byte(identity))
	key := Version + "-"
	if slug := readableSlug(identity); slug != "" {
		key += slug + "-"
	}
	key += hex.EncodeToString(sum[:])
	if err := ValidateComponent(key); err != nil {
		return "", err
	}
	return key, nil
}

// ValidateComponent rejects empty, dot, dotdot, separator-containing, aside-
// prefixed, overlong and non-ASCII-safe components. It validates a component,
// not enrollment or correspondence between a persisted key and an identity.
// ASCII letters, digits, dots, underscores and hyphens are allowed.
func ValidateComponent(component string) error {
	if component == "" || component == "." || component == ".." ||
		strings.HasPrefix(component, ".prev-") || len(component) > MaxKeyLength {
		return ErrInvalidComponent
	}
	for _, c := range component {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return fmt.Errorf("%w: unsupported character", ErrInvalidComponent)
		}
	}
	return nil
}

func readableSlug(identity string) string {
	var b strings.Builder
	// Stop once the bounded prefix is full. The digest still covers all bytes.
	for _, c := range identity {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_') {
			c = '-'
		}
		if c == '-' && (b.Len() == 0 || strings.HasSuffix(b.String(), "-")) {
			continue
		}
		b.WriteByte(byte(c))
		if b.Len() == MaxSlugLength {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}
