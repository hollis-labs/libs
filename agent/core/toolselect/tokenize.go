package toolselect

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// builtinStopwords is the 46-word list shared verbatim by go-toolbroker's
// discovery scorer and Nanite's intent scorer.
var builtinStopwords = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(`the and for are but not you all can her
		was one our out has its let get make like
		just want need from with this that have will what
		when how who which where why been being would could
		should about into some any`) {
		builtinStopwords[w] = struct{}{}
	}
}

// tokenize lower-cases s and splits it on every rune that is not a letter or
// digit (so "torque_task-create" yields three tokens). Tokens shorter than two
// runes are dropped. Stopwords are not filtered here.
func tokenize(s string) []string {
	parts := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := parts[:0]
	for _, p := range parts {
		if utf8.RuneCountInString(p) >= 2 {
			out = append(out, p)
		}
	}
	return out
}
