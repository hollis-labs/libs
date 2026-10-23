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
			s := tsQuote(t.Text)
			label := ""
			if t.Prefix {
				label += "*"
			}
			if t.Field != "" {
				label += string(fs.letters[t.Field])
			}
			if label != "" {
				s += ":" + label
			}
			return s, nil
		},
		and: func(p []string) string { return paren(p, " & ") },
		or:  func(p []string) string { return paren(p, " | ") },
		subtract: func(pos string, neg []string) string {
			return "(" + pos + " & !" + paren(neg, " | ") + ")"
		},
	}
}
