package router_test

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/core/util/router"
)

const rules = `{
  "version": "2026-10-16.1",
  "strategy": "first_match",
  "rules": [
    {"id": "save-note", "priority": 10,
     "when": [{"field": "kind", "op": "eq", "value": "note"}, {"field": "action", "op": "eq", "value": "save"}],
     "target": "notes.write", "auto": true, "min_confidence": 0.8},
    {"id": "links", "when": [{"field": "url", "op": "glob", "value": "https://example.com/*"}],
     "target": "link-fetcher", "auto": true}
  ]}`

func compile() *router.Router {
	set, err := router.ParseRuleSet([]byte(rules))
	if err != nil {
		panic(err)
	}
	r, err := router.Compile(set, router.Config{})
	if err != nil {
		panic(err)
	}
	return r
}

func ExampleRouter_Route() {
	r := compile()
	d, _ := r.Route(context.Background(), router.Subject{
		ID:     "msg-1",
		Fields: map[string]string{"url": "https://example.com/a/b"},
	})
	fmt.Println(d.Outcome, d.Matches[0].Target, d.Matches[0].Disposition)
	// Output:
	// routed link-fetcher apply
}

func ExampleRouter_Route_confidenceGate() {
	r := compile()
	fields := map[string]string{"kind": "note", "action": "save"}

	high, low := 0.95, 0.4
	for _, conf := range []*float64{&high, &low, nil} {
		d, _ := r.Route(context.Background(), router.Subject{ID: "msg-2", Fields: fields, Confidence: conf})
		fmt.Println(d.Outcome, d.Matches[0].Disposition, "-", d.Matches[0].Why)
	}
	// Output:
	// routed apply - matched
	// suggested ask - confidence 0.40 < 0.80 -> ask
	// suggested ask - confidence unknown, rule needs >= 0.80 -> ask
}
