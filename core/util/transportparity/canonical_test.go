package transportparity

import (
	"encoding/json"
	"strings"
	"testing"
)

func canon(t *testing.T, v any) string {
	t.Helper()
	return CanonicalJSON(t, v)
}

func TestCanonicalJSONIsIndependentOfKeyOrderAndRepresentation(t *testing.T) {
	type item struct {
		Zebra string `json:"zebra"`
		Alpha int    `json:"alpha"`
	}
	one := item{Zebra: "z", Alpha: 1}
	want := canon(t, map[string]any{"alpha": 1, "zebra": "z"})
	// A struct marshals in field order (zebra first); a map marshals sorted.
	if got := canon(t, one); got != want {
		t.Errorf("struct = %s, map = %s", got, want)
	}
	// A pointer and the value it points to are the same.
	if got := canon(t, &one); got != want {
		t.Errorf("pointer = %s, want %s", got, want)
	}
	// Nested objects are sorted too, and so are objects inside arrays.
	a := canon(t, map[string]any{"list": []any{map[string]any{"b": 1, "a": 2}}})
	b := canon(t, json.RawMessage(`{"list":[{"a":2,"b":1}]}`))
	if a != b {
		t.Errorf("nested: %s vs %s", a, b)
	}
}

// The point of the helper: it is NOT byte-for-byte. The raw marshaled forms of
// these two differ, and the canonical forms do not.
func TestCanonicalJSONIsNotByteForByte(t *testing.T) {
	type s struct {
		B int `json:"b"`
		A int `json:"a"`
	}
	raw1, _ := json.Marshal(s{B: 2, A: 1})
	raw2, _ := json.Marshal(map[string]any{"a": 1, "b": 2})
	if string(raw1) == string(raw2) {
		t.Fatal("test premise broken: raw forms are already equal")
	}
	if canon(t, s{B: 2, A: 1}) != canon(t, map[string]any{"a": 1, "b": 2}) {
		t.Error("canonical forms differ")
	}
}

func TestCanonicalJSONNormalizesNumbers(t *testing.T) {
	same := []string{`1`, `1.0`, `1e0`, `1.00`}
	want := canon(t, json.RawMessage(same[0]))
	for _, s := range same {
		if got := canon(t, json.RawMessage(s)); got != want {
			t.Errorf("%s -> %s, want %s", s, got, want)
		}
	}
	if canon(t, 1) != canon(t, 1.0) {
		t.Error("int 1 and float 1.0 differ")
	}
	// Distinct values stay distinct, including integers past float64's range.
	if canon(t, json.RawMessage(`9007199254740993`)) == canon(t, json.RawMessage(`9007199254740992`)) {
		t.Error("two different large integers were collapsed")
	}
	if canon(t, 0.5) == canon(t, 0.25) {
		t.Error("two different fractions were collapsed")
	}
}

func TestCanonicalJSONKeepsTypesApart(t *testing.T) {
	for _, pair := range [][2]any{
		{"1", 1},
		{true, "true"},
		{nil, "null"},
		{[]any{}, map[string]any{}},
		{[]any{1, 2}, []any{2, 1}}, // array order is meaningful
	} {
		if canon(t, pair[0]) == canon(t, pair[1]) {
			t.Errorf("%#v and %#v compare equal", pair[0], pair[1])
		}
	}
}

func TestCanonicalJSONReportsAnUnmarshalableValue(t *testing.T) {
	rec := &recorder{}
	CanonicalJSON(rec, make(chan int))
	if !rec.fatal || !strings.Contains(rec.text(), "cannot marshal") {
		t.Errorf("got %q (fatal=%v)", rec.text(), rec.fatal)
	}
}
