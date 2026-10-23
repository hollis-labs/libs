package search

import (
	"errors"
	"strings"
	"unicode"
)

// Expr is a node of the portable query model.
type Expr interface{ isExpr() }

// Term is one word, or a phrase when Phrase is set or Text holds several words.
type Term struct {
	Text string
	// Prefix matches words (or, for a phrase, the last word) starting with Text.
	Prefix bool
	// Phrase requires the words of Text adjacent and in order.
	Phrase bool
	// Field restricts the term to one index field. Empty searches all fields.
	Field string
}

// And requires every child. Not children subtract from the positive ones.
type And []Expr

// Or requires at least one child.
type Or []Expr

// Not excludes matches of X. It is valid only as a child of And that also has a
// positive child.
type Not struct{ X Expr }

func (Term) isExpr() {}
func (And) isExpr()  {}
func (Or) isExpr()   {}
func (Not) isExpr()  {}

// Errors.
var (
	// ErrInvalidQuery is a structurally invalid Expr built by the caller (a
	// standalone Not, an unknown field). Text from [Parse] never produces it.
	ErrInvalidQuery = errors.New("search: invalid query")
	// ErrInvalidIndex is a bad Index definition.
	ErrInvalidIndex = errors.New("search: invalid index")
)

// ParseOptions tunes [Parse].
type ParseOptions struct {
	// Any makes adjacent terms OR instead of AND (recall over precision).
	Any bool
	// NoOperators treats AND, OR, NOT, parentheses and "-" as plain text.
	NoOperators bool
	// Fields lists the field names usable as "field:term". Unknown "a:b" stays
	// a plain term.
	Fields []string
	// PrefixLast makes the final bare term a prefix (typeahead).
	PrefixLast bool
}

type tok struct {
	kind byte // 'w' word, 'p' phrase, '(' , ')'
	text string
}

func tokenize(s string, noOps bool) []tok {
	var out []tok
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '"':
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				j++
			}
			out = append(out, tok{'p', string(rs[i+1 : j])})
			i = j + 1
		case (r == '(' || r == ')') && !noOps:
			out = append(out, tok{byte(r), ""})
			i++
		default:
			j := i
			for j < len(rs) && !unicode.IsSpace(rs[j]) && rs[j] != '"' && !((rs[j] == '(' || rs[j] == ')') && !noOps) {
				j++
			}
			out = append(out, tok{'w', string(rs[i:j])})
			i = j
		}
	}
	return out
}

type parser struct {
	toks   []tok
	pos    int
	opts   ParseOptions
	fields map[string]bool
	depth  int
}

// Parse turns user text into an Expr. It accepts words, "quoted phrases",
// prefix* terms, AND / OR / NOT (upper case), -word, (groups) and field:term. It
// never fails: input it cannot make sense of becomes ordinary terms, and empty
// or punctuation-only input becomes a nil Expr (which matches nothing).
//
//	grammar:  or  = and { "OR" and }
//	          and = unary { ["AND"] unary }
//	          unary = ("NOT" | "-") unary | atom
//	          atom = "(" or ")" | phrase | [field ":"] word ["*"]
func Parse(text string, opts ParseOptions) Expr {
	text = clean(text)
	toks := tokenize(text, opts.NoOperators)
	if opts.PrefixLast && len(toks) > 0 {
		// Typeahead: the user is mid-word unless the text ends in a separator.
		last := &toks[len(toks)-1]
		end := []rune(text)[len([]rune(text))-1]
		if last.kind == 'w' && !unicode.IsSpace(end) && end != '*' && last.text != "AND" && last.text != "OR" && last.text != "NOT" {
			last.text += "*"
		}
	}
	p := &parser{toks: toks, opts: opts, fields: map[string]bool{}}
	for _, f := range opts.Fields {
		p.fields[f] = true
	}
	e := p.or()
	if p.pos < len(p.toks) { // unbalanced ")": treat the rest as more terms
		rest := &parser{toks: p.toks[p.pos+1:], opts: opts, fields: p.fields}
		if r := rest.or(); r != nil {
			e = join(opts.Any, e, r)
		}
	}
	return normalize(e)
}

func join(any bool, a, b Expr) Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case any:
		return Or{a, b}
	}
	return And{a, b}
}

const maxDepth = 16

func (p *parser) peek() (tok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return tok{}, false
}

func (p *parser) isKeyword(t tok, kw string) bool {
	return !p.opts.NoOperators && t.kind == 'w' && t.text == kw
}

func (p *parser) or() Expr {
	var alts Or
	if e := p.and(); e != nil {
		alts = append(alts, e)
	}
	for {
		t, ok := p.peek()
		if !ok || !p.isKeyword(t, "OR") {
			break
		}
		p.pos++
		if e := p.and(); e != nil {
			alts = append(alts, e)
		}
	}
	switch len(alts) {
	case 0:
		return nil
	case 1:
		return alts[0]
	}
	return alts
}

func (p *parser) and() Expr {
	var items []Expr
	for {
		t, ok := p.peek()
		if !ok || t.kind == ')' || p.isKeyword(t, "OR") {
			break
		}
		if p.isKeyword(t, "AND") {
			p.pos++
			if t2, ok := p.peek(); !ok || t2.kind == ')' || p.isKeyword(t2, "OR") {
				break // dangling AND
			}
		}
		if e := p.unary(); e != nil {
			items = append(items, e)
		}
	}
	switch len(items) {
	case 0:
		return nil
	case 1:
		return items[0]
	}
	if p.opts.Any {
		// Adjacent terms are OR in recall mode; negatives still subtract.
		var pos, neg []Expr
		for _, it := range items {
			if n, ok := it.(Not); ok {
				neg = append(neg, n)
			} else {
				pos = append(pos, it)
			}
		}
		if len(neg) == 0 {
			return Or(pos)
		}
		var head Expr = Or(pos)
		if len(pos) == 1 {
			head = pos[0]
		}
		return And(append([]Expr{head}, neg...))
	}
	return And(items)
}

func (p *parser) unary() Expr {
	t, ok := p.peek()
	if !ok {
		return nil
	}
	if p.isKeyword(t, "NOT") {
		p.pos++
		if n, ok := p.peek(); !ok || n.kind == ')' {
			return Term{Text: "NOT"}
		}
		if x := p.unary(); x != nil {
			return Not{X: x}
		}
		return nil
	}
	if !p.opts.NoOperators && t.kind == 'w' && len(t.text) > 1 && t.text[0] == '-' {
		p.toks[p.pos].text = t.text[1:]
		if x := p.unary(); x != nil {
			return Not{X: x}
		}
		return nil
	}
	return p.atom()
}

func (p *parser) atom() Expr {
	t, ok := p.peek()
	if !ok {
		return nil
	}
	p.pos++
	switch t.kind {
	case '(':
		if p.depth >= maxDepth {
			return nil
		}
		p.depth++
		e := p.or()
		p.depth--
		if n, ok := p.peek(); ok && n.kind == ')' {
			p.pos++
		}
		return e
	case ')':
		return nil
	case 'p':
		if strings.TrimSpace(t.text) == "" {
			return nil
		}
		return Term{Text: strings.TrimSpace(t.text), Phrase: true}
	}
	text := t.text
	term := Term{}
	if i := strings.IndexByte(text, ':'); i > 0 && p.fields[text[:i]] && i < len(text)-1 {
		term.Field = text[:i]
		text = text[i+1:]
	}
	if strings.HasSuffix(text, "*") && len(text) > 1 {
		term.Prefix = true
		text = strings.TrimRight(text, "*")
	}
	if strings.TrimFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) == "" {
		return nil // punctuation only: nothing to search for
	}
	term.Text = text
	return term
}

// normalize drops what cannot be compiled: pure-negative branches of an Or and
// a standalone Not, so user text never reaches the compiler in an invalid shape.
func normalize(e Expr) Expr {
	switch x := e.(type) {
	case nil:
		return nil
	case Term:
		return x
	case Not:
		return nil
	case Or:
		var out Or
		for _, c := range x {
			if n := normalize(c); n != nil {
				out = append(out, n)
			}
		}
		switch len(out) {
		case 0:
			return nil
		case 1:
			return out[0]
		}
		return out
	case And:
		var pos, neg []Expr
		for _, c := range x {
			if n, ok := c.(Not); ok {
				if inner := normalize(n.X); inner != nil {
					neg = append(neg, Not{X: inner})
				}
				continue
			}
			if n := normalize(c); n != nil {
				pos = append(pos, n)
			}
		}
		if len(pos) == 0 {
			return nil
		}
		all := append(pos, neg...)
		if len(all) == 1 {
			return all[0]
		}
		return And(all)
	}
	return nil
}

// clean removes what no engine can take in a query: NUL bytes and invalid
// UTF-8.
func clean(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}
