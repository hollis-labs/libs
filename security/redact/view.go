package redact

import (
	"unicode"
	"unicode/utf8"
)

// view is the text the rules run on, with a map back to the original bytes. Rules
// should see "password" even when it is written with a zero-width character
// inside it or in fullwidth letters, but what gets replaced is the original text.
type view struct {
	text string
	// orig[i] is the byte offset in the original of normalized byte i; the last
	// element is len(original). nil means the view is the original (the fast path).
	orig []int
}

// newView normalizes s: format characters (zero-width joiners, bidi controls) are
// dropped and fullwidth ASCII (U+FF01..U+FF5E) is folded to ASCII.
func newView(s string) view {
	if plain(s) {
		return view{text: s}
	}
	out := make([]byte, 0, len(s))
	orig := make([]int, 0, len(s)+1)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			out = append(out, s[i])
			orig = append(orig, i)
		case unicode.Is(unicode.Cf, r):
			// dropped: contributes no byte to the view
		case r >= 0xFF01 && r <= 0xFF5E:
			out = append(out, byte(r-0xFF01+0x21))
			orig = append(orig, i)
		default:
			for k := 0; k < n; k++ {
				out = append(out, s[i+k])
				orig = append(orig, i)
			}
		}
		i += n
	}
	orig = append(orig, len(s))
	return view{text: string(out), orig: orig}
}

// plain reports text that needs no normalization: nothing to drop or fold.
func plain(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			for _, r := range s {
				if unicode.Is(unicode.Cf, r) || (r >= 0xFF01 && r <= 0xFF5E) {
					return false
				}
			}
			return true
		}
	}
	return true
}

// at maps a byte offset of the view to the original.
func (v view) at(i int) int {
	if v.orig == nil {
		return i
	}
	if i >= len(v.orig) {
		return v.orig[len(v.orig)-1]
	}
	return v.orig[i]
}
