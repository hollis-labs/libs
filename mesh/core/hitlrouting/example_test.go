package hitlrouting_test

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/core/util/router"
	"github.com/hollis-labs/libs/mesh/core/hitlrouting"
)

func Example() {
	profile, err := hitlrouting.Parse([]byte(`{
	  "version": "2026-10-23.1",
	  "rules": [
	    {"id": "prod-shell", "priority": 10,
	     "match": {"kind": "approval", "tool": "shell", "namespace": "prod/*"},
	     "sink": "ops-console", "completion": "wait", "delivery": "await"}
	  ],
	  "catch_all": {"sink": "tachyon", "completion": "async", "delivery": "durable_message"}
	}`))
	if err != nil {
		panic(err)
	}
	r, err := hitlrouting.Compile(profile, router.Config{})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	a, _ := r.Resolve(ctx, "card-1", hitlrouting.Subject{Kind: "approval", Tool: "shell", Namespace: "prod/eu"})
	b, _ := r.Resolve(ctx, "card-2", hitlrouting.Subject{Kind: "form"})
	fmt.Println(a.Sink, a.Completion, a.Delivery, a.RuleID)
	fmt.Println(b.Sink, b.Completion, b.Delivery, b.RuleID)
	// Output:
	// ops-console wait await prod-shell
	// tachyon async durable_message catch_all
}
