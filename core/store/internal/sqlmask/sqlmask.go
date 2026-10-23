// Package sqlmask is the SQL tokenizer shared by the placeholder rebinder and
// the portability lint. It is internal so its shape can change.
package sqlmask

import "strings"

// Span is a run of a statement: code, or a literal (string, quoted identifier,
// comment, dollar-quoted body).
type Span struct {
	From, To int
	Literal  bool
}

func IsIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// Scan splits a statement into code and literal spans. It understands
// '...' strings (and E'...' strings with backslash escapes), "..." identifiers,
// -- line comments, nested /* */ block comments and $tag$...$tag$ bodies. A "$"
// opens a dollar quote only when the byte before it is not an identifier
// character, so a$b$c is one identifier.
func Scan(s string) []Span {
	var out []Span
	codeFrom := 0
	flush := func(to int) {
		if to > codeFrom {
			out = append(out, Span{codeFrom, to, false})
		}
	}
	lit := func(from, to int) {
		flush(from)
		out = append(out, Span{from, to, true})
		codeFrom = to
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			esc := i > 0 && (s[i-1] == 'E' || s[i-1] == 'e') && (i < 2 || !IsIdentByte(s[i-2]))
			j := skipString(s, i, esc)
			lit(i, j)
			i = j
		case c == '"':
			j := skipQuoted(s, i, '"')
			lit(i, j)
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			lit(i, i+j)
			i += j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := skipBlockComment(s, i)
			lit(i, j)
			i = j
		case c == '$' && (i == 0 || !IsIdentByte(s[i-1])):
			if tag, ok := dollarTag(s, i); ok {
				j := len(s)
				if end := strings.Index(s[i+len(tag):], tag); end >= 0 {
					j = i + len(tag) + end + len(tag)
				}
				lit(i, j)
				i = j
				continue
			}
			i++
		default:
			i++
		}
	}
	flush(len(s))
	return out
}

// skipString returns the index past a single-quoted string starting at i. With
// esc (an E” string) a backslash escapes the next byte.
func skipString(s string, i int, esc bool) int {
	for j := i + 1; j < len(s); j++ {
		switch {
		case esc && s[j] == '\\':
			j++
		case s[j] == '\'':
			if j+1 < len(s) && s[j+1] == '\'' {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// skipBlockComment returns the index past a (nestable) block comment at i.
func skipBlockComment(s string, i int) int {
	depth := 0
	for j := i; j < len(s)-1; {
		switch {
		case s[j] == '/' && s[j+1] == '*':
			depth++
			j += 2
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(s)
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

// Mask returns query with string literals, quoted identifiers, comments and
// dollar-quoted bodies replaced by spaces, keeping offsets stable. The lint
// package uses it so a banned word inside a literal does not trigger.
func Mask(query string) string {
	out := []byte(query)
	for _, sp := range Scan(query) {
		if !sp.Literal {
			continue
		}
		for k := sp.From; k < sp.To; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	return string(out)
}
