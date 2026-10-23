package store

import (
	"strconv"
	"strings"
)

// Rebind rewrites "?" placeholders to "$1", "$2", ... for Postgres. It walks
// the statement with a small tokenizer, so a "?" inside a string literal, a
// quoted identifier, a comment or a dollar-quoted body is left alone. "??" is
// an escaped literal "?".
func Rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			j := skipQuoted(query, i, c)
			b.WriteString(query[i:j])
			i = j
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = len(query) - i
			}
			b.WriteString(query[i : i+j])
			i += j
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			j := strings.Index(query[i+2:], "*/")
			if j < 0 {
				j = len(query) - i
			} else {
				j += 4
			}
			b.WriteString(query[i : i+j])
			i += j
		case c == '$':
			if tag, ok := dollarTag(query, i); ok {
				end := strings.Index(query[i+len(tag):], tag)
				j := len(query)
				if end >= 0 {
					j = i + len(tag) + end + len(tag)
				}
				b.WriteString(query[i:j])
				i = j
				continue
			}
			b.WriteByte(c)
			i++
		case c == '?':
			if i+1 < len(query) && query[i+1] == '?' {
				b.WriteByte('?')
				i += 2
				continue
			}
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipQuoted returns the index just past a quoted run starting at i. A doubled
// quote character is an escape inside the run.
func skipQuoted(s string, i int, q byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != q {
			continue
		}
		if j+1 < len(s) && s[j+1] == q {
			j++
			continue
		}
		return j + 1
	}
	return len(s)
}

// dollarTag reports a dollar-quote opener ($$ or $tag$) at i.
func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || j > i+1 && s[j] >= '0' && s[j] <= '9') {
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1], true
	}
	return "", false
}

// Mask returns query with string literals, quoted identifiers and comments
// replaced by spaces, keeping offsets stable. The lint package uses it so a
// banned word inside a literal does not trigger.
func Mask(query string) string {
	out := []byte(query)
	blank := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < len(query); {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			j := skipQuoted(query, i, c)
			blank(i, j)
			i = j
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = len(query) - i
			}
			blank(i, i+j)
			i += j
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			j := strings.Index(query[i+2:], "*/")
			if j < 0 {
				j = len(query) - i
			} else {
				j += 4
			}
			blank(i, i+j)
			i += j
		default:
			i++
		}
	}
	return string(out)
}
