package search_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/core/store/search"
)

func show(e search.Expr) string {
	switch x := e.(type) {
	case nil:
		return "<nil>"
	case search.Term:
		s := x.Text
		if x.Phrase {
			s = `"` + s + `"`
		}
		if x.Prefix {
			s += "*"
		}
		if x.Field != "" {
			s = x.Field + ":" + s
		}
		return s
	case search.And:
		return "AND(" + join(x) + ")"
	case search.Or:
		return "OR(" + join(x) + ")"
	case search.Not:
		return "NOT(" + show(x.X) + ")"
	}
	return fmt.Sprintf("?%T", e)
}

func join(xs []search.Expr) string {
	var p []string
	for _, x := range xs {
		p = append(p, show(x))
	}
	return strings.Join(p, " ")
}

func TestParse(t *testing.T) {
	fields := []string{"title", "body"}
	cases := []struct {
		in   string
		opts search.ParseOptions
		want string
	}{
		{"fox", search.ParseOptions{}, "fox"},
		{"lazy dog", search.ParseOptions{}, "AND(lazy dog)"},
		{"lazy dog", search.ParseOptions{Any: true}, "OR(lazy dog)"},
		{`"lazy dog"`, search.ParseOptions{}, `"lazy dog"`},
		{"ind*", search.ParseOptions{}, "ind*"},
		{"fox OR dog", search.ParseOptions{}, "OR(fox dog)"},
		{"a b OR c", search.ParseOptions{}, "OR(AND(a b) c)"},
		{"a AND b", search.ParseOptions{}, "AND(a b)"},
		{"lazy NOT cat", search.ParseOptions{}, "AND(lazy NOT(cat))"},
		{"-cat lazy", search.ParseOptions{}, "AND(lazy NOT(cat))"},
		{"(a OR b) c", search.ParseOptions{}, "AND(OR(a b) c)"},
		{"title:fox", search.ParseOptions{Fields: fields}, "title:fox"},
		{"nope:fox", search.ParseOptions{Fields: fields}, "nope:fox"},
		{"laz", search.ParseOptions{PrefixLast: true}, "laz*"},
		{"laz ", search.ParseOptions{PrefixLast: true}, "laz"},
		{"fox AND", search.ParseOptions{}, "fox"},
		{"OR fox", search.ParseOptions{}, "fox"},
		{"NOT", search.ParseOptions{}, "NOT"},
		{"NOT cat", search.ParseOptions{}, "<nil>"},
		{"a OR NOT b", search.ParseOptions{}, "a"},
		{"fox and dog", search.ParseOptions{}, "AND(fox and dog)"},
		{"fox OR dog", search.ParseOptions{NoOperators: true}, "AND(fox OR dog)"},
		{"", search.ParseOptions{}, "<nil>"},
		{"   ", search.ParseOptions{}, "<nil>"},
		{"!!! ???", search.ParseOptions{}, "<nil>"},
		{`"`, search.ParseOptions{}, "<nil>"},
		{`"open phrase`, search.ParseOptions{}, `"open phrase"`},
		{")(", search.ParseOptions{}, "<nil>"},
		{"a )", search.ParseOptions{}, "a"},
		{"(((((((((((((((((((a)))))))))))))))))))", search.ParseOptions{}, "a"}, // depth-capped, never recursion-bombed
	}
	for _, c := range cases {
		if got := show(search.Parse(c.in, c.opts)); got != c.want {
			t.Errorf("Parse(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func FuzzParseNeverPanics(f *testing.F) {
	for _, s := range []string{`a OR "b c" -d (e`, "))((", `"`, "NOT NOT NOT", "title:", "***", "a AND OR NOT b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = search.Parse(s, search.ParseOptions{Fields: []string{"title"}, PrefixLast: true})
		_ = search.Parse(s, search.ParseOptions{Any: true})
	})
}
