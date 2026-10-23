package search

import (
	"fmt"
	"strings"
)

// fieldSet resolves field names for the compilers.
type fieldSet struct {
	names   []string
	letters map[string]byte // postgres weight label per field
}

func (f fieldSet) has(name string) bool {
	for _, n := range f.names {
		if n == name {
			return true
		}
	}
	return false
}

// split separates positive and negated children of an And.
func split(a And) (pos, neg []Expr) {
	for _, c := range a {
		if n, ok := c.(Not); ok {
			neg = append(neg, n.X)
		} else {
			pos = append(pos, c)
		}
	}
	return
}

// compiler renders an Expr for one engine.
type compiler struct {
	term     func(Term) (string, error)
	and      func(pos []string) string
	or       func(alts []string) string
	subtract func(pos string, neg []string) string
	fields   fieldSet
}

func (c compiler) compile(e Expr) (string, error) {
	switch x := e.(type) {
	case Term:
		x.Text = clean(x.Text)
		if strings.TrimSpace(x.Text) == "" {
			return "", fmt.Errorf("%w: empty term", ErrInvalidQuery)
		}
		if x.Field != "" && !c.fields.has(x.Field) {
			return "", fmt.Errorf("%w: unknown field %q", ErrInvalidQuery, x.Field)
		}
		return c.term(x)
	case Or:
		if len(x) == 0 {
			return "", fmt.Errorf("%w: empty OR", ErrInvalidQuery)
		}
		parts := make([]string, 0, len(x))
		for _, ch := range x {
			s, err := c.compile(ch)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return c.or(parts), nil
	case And:
		pos, neg := split(x)
		if len(pos) == 0 {
			return "", fmt.Errorf("%w: NOT needs a positive term beside it", ErrInvalidQuery)
		}
		ps := make([]string, 0, len(pos))
		for _, ch := range pos {
			s, err := c.compile(ch)
			if err != nil {
				return "", err
			}
			ps = append(ps, s)
		}
		head := c.and(ps)
		if len(neg) == 0 {
			return head, nil
		}
		ns := make([]string, 0, len(neg))
		for _, ch := range neg {
			s, err := c.compile(ch)
			if err != nil {
				return "", err
			}
			ns = append(ns, s)
		}
		return c.subtract(head, ns), nil
	case Not:
		return "", fmt.Errorf("%w: NOT needs a positive term beside it", ErrInvalidQuery)
	}
	return "", fmt.Errorf("%w: unsupported node %T", ErrInvalidQuery, e)
}

// ---- FTS5 ----

func fts5Quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func fts5Compiler(fs fieldSet) compiler {
	paren := func(parts []string, op string) string {
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, op) + ")"
	}
	return compiler{
		fields: fs,
		term: func(t Term) (string, error) {
			s := fts5Quote(t.Text)
			if t.Prefix {
				s += "*"
			}
			if t.Field != "" {
				s = t.Field + " : " + s
			}
			return s, nil
		},
		and: func(p []string) string { return paren(p, " AND ") },
		or:  func(p []string) string { return paren(p, " OR ") },
		subtract: func(pos string, neg []string) string {
			return "(" + pos + " NOT " + paren(neg, " OR ") + ")"
		},
	}
}

// ---- Postgres tsquery ----

func tsQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

// tagFold mirrors the translate() in the generated vector: the parser would
// read "<b>" as a tag.
var tagFold = strings.NewReplacer("<", " ", ">", " ")

func tsCompiler(fs fieldSet) compiler {
	paren := func(parts []string, op string) string {
		if len(parts) == 1 {
			return parts[0]
		}
		return "(" + strings.Join(parts, op) + ")"
	}
	return compiler{
		fields: fs,
		term: func(t Term) (string, error) {
			text := strings.TrimSpace(tagFold.Replace(t.Text))
			if text == "" {
				return "", fmt.Errorf("%w: empty term", ErrInvalidQuery)
			}
			label := ""
			if t.Prefix {
				label += "*"
			}
			if t.Field != "" {
				label += string(fs.letters[t.Field])
			}
			suffix := ""
			if label != "" {
				suffix = ":" + label
			}
			// A quoted multi-word term is a phrase. With a prefix, only the last
			// word is a prefix (as in FTS5's "brown fo"*), so the words before it
			// are joined with <-> and the label goes on the last word alone.
			words := strings.Fields(text)
			if t.Prefix && len(words) > 1 {
				head := make([]string, len(words)-1)
				for i, w := range words[:len(words)-1] {
					head[i] = tsQuote(w)
					if t.Field != "" {
						head[i] += ":" + string(fs.letters[t.Field])
					}
				}
				return "(" + strings.Join(head, " <-> ") + " <-> " + tsQuote(words[len(words)-1]) + suffix + ")", nil
			}
			return tsQuote(text) + suffix, nil
		},
		and: func(p []string) string { return paren(p, " & ") },
		or:  func(p []string) string { return paren(p, " | ") },
		subtract: func(pos string, neg []string) string {
			return "(" + pos + " & !" + paren(neg, " | ") + ")"
		},
	}
}
