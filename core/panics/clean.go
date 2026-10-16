package panics

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clean makes untrusted text safe to log or store: invalid UTF-8 is repaired,
// control characters and format characters (zero-width joiners, bidi overrides)
// are replaced by a space, and the result is bounded to limit runes without
// splitting one. Newlines and tabs are kept. It does not remove secrets: that
// is the redactor's job.
func Clean(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= limit {
			b.WriteString("…")
			break
		}
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
		n++
	}
	out := strings.TrimSpace(b.String())
	if !utf8.ValidString(out) {
		return strings.ToValidUTF8(out, "�")
	}
	return out
}
