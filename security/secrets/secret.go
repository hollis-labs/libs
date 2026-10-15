package secrets

import (
	"fmt"
	"log/slog"
)

// Redacted is what a [Secret] prints as.
const Redacted = "[redacted]"

// Secret holds secret material. It prints as [Redacted] through fmt, JSON,
// text marshaling and slog; use [Secret.Reveal] or [Secret.Bytes] to get the
// value, at the call that needs it.
//
// The zero Secret is empty. Secret is a small value type: copies share the
// same backing bytes until [Secret.Zero] is called on one of them.
type Secret struct {
	b []byte
}

// FromString returns a Secret holding a copy of s.
func FromString(s string) Secret { return Secret{b: []byte(s)} }

// FromBytes returns a Secret holding a copy of b.
func FromBytes(b []byte) Secret { return Secret{b: append([]byte(nil), b...)} }

// Reveal returns the secret as a string.
func (s Secret) Reveal() string { return string(s.b) }

// Bytes returns a copy of the secret.
func (s Secret) Bytes() []byte { return append([]byte(nil), s.b...) }

// Len returns the length of the secret in bytes.
func (s Secret) Len() int { return len(s.b) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return len(s.b) == 0 }

// Zero overwrites the secret's bytes. Best effort: Go gives no guarantee that
// no other copy exists, for example in a string made by Reveal.
func (s *Secret) Zero() {
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

// String implements fmt.Stringer.
func (Secret) String() string { return Redacted }

// GoString implements fmt.GoStringer.
func (Secret) GoString() string { return Redacted }

// Format implements fmt.Formatter so that no verb, %x and %q included, can
// print the value.
func (Secret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(Redacted)) }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler.
func (Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(Redacted) }
