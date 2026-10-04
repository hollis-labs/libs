package keymerge

import (
	"reflect"
	"strings"
	"testing"
)

func TestObserveExactLeavesPreservesNullAndAbsence(t *testing.T) {
	states, err := ObserveKeys("json", []byte(`{"a":null,"b":{},"list":[{"x":1}]}`), []KeyPath{{"a"}, {"b"}, {"missing"}, {"list"}})
	if err != nil {
		t.Fatal(err)
	}
	if !states[0].Exists || !states[1].Exists || states[2].Exists || !states[3].Exists || states[0].Digest == states[1].Digest || states[0].Digest == "" {
		t.Fatal("collapsed null/empty/absence")
	}
	again, err := ObserveKeys("json", []byte(" { \"a\" : null,\"b\":{},\"list\":[{\"x\":1}] } "), []KeyPath{{"a"}, {"b"}, {"missing"}, {"list"}})
	if err != nil || !reflect.DeepEqual(states, again) {
		t.Fatal("whitespace changed semantic leaf evidence")
	}
	states[0].Path[0] = "changed"
	if again[0].Path[0] != "a" {
		t.Fatal("path aliases")
	}
}
func TestObserveRejectsAmbiguousOrUnreadableDocument(t *testing.T) {
	invalid := [][]byte{[]byte{}, []byte("[]"), []byte("null"), []byte(`{"a":1} {}`), []byte(`{"operator":{"x":1,"x":2}}`), []byte(`{"operator":[{"x":1,"x":2}]}`), []byte(`{"a":{"leaf":1}}`), []byte("{\"operator\":\"\xff\"}")}
	for _, raw := range invalid {
		if _, err := ObserveKeys("json", raw, []KeyPath{{"a"}}); err == nil {
			t.Fatalf("accepted ambiguous document %q", raw)
		}
	}
	if _, err := ObserveKeys("json", []byte(`{"a":"operator"}`), []KeyPath{{"a", "leaf"}}); err == nil {
		t.Fatal("accepted scalar ancestor")
	}
	if _, err := ObserveKeys("toml", []byte("a=1\na=2\n"), []KeyPath{{"a"}}); err == nil {
		t.Fatal("accepted duplicate TOML")
	}
	if _, err := ObserveKeys("unknown", []byte("{}"), []KeyPath{{"a"}}); err == nil {
		t.Fatal("guessed encoding")
	}
}

func TestObserveDeepUnownedJSONKeepsExactLeaf(t *testing.T) {
	raw := []byte(`{"owned":1,"operator":` + strings.Repeat(`[`, 3000) + `{"x":"preserved"}` + strings.Repeat(`]`, 3000) + `}`)
	states, err := ObserveKeys("json", raw, []KeyPath{{"owned"}})
	simple, simpleErr := ObserveKeys("json", []byte(`{"owned":1}`), []KeyPath{{"owned"}})
	if err != nil || simpleErr != nil || !reflect.DeepEqual(states, simple) {
		t.Fatal("unowned nested JSON changed leaf evidence", err)
	}
}
