package store

import (
	"strconv"
	"strings"

	"github.com/hollis-labs/libs/core/store/internal/sqlmask"
)

// Rebind rewrites "?" placeholders to "$1", "$2", ... for Postgres. It walks
// the statement with a small tokenizer, so a "?" inside a string literal, a
// quoted identifier, a comment or a dollar-quoted body is left alone. "??" is
// an escaped literal "?".
func Rebind(query string) string { return rebind(query, true) }

// rebindSQLite applies the same tokenizer for SQLite: placeholders stay "?",
// and only "??" outside literals and comments collapses to a literal "?".
func rebindSQLite(query string) string { return rebind(query, false) }

func rebind(query string, numbered bool) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for _, sp := range sqlmask.Scan(query) {
		text := query[sp.From:sp.To]
		if sp.Literal {
			b.WriteString(text)
			continue
		}
		for i := 0; i < len(text); i++ {
			c := text[i]
			if c != '?' {
				b.WriteByte(c)
				continue
			}
			if i+1 < len(text) && text[i+1] == '?' {
				b.WriteByte('?')
				i++
				continue
			}
			if numbered {
				n++
				b.WriteByte('$')
				b.WriteString(strconv.Itoa(n))
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}
