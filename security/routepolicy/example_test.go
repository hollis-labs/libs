package routepolicy_test

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/core/util/router"
	"github.com/hollis-labs/libs/security/policy"
	"github.com/hollis-labs/libs/security/routepolicy"
)

// askForExternal allows routes to internal targets and requires approval for others.
type askForExternal struct{}

func (askForExternal) Evaluate(_ context.Context, req policy.Request) (policy.Decision, error) {
	match := []policy.PolicyMatch{{ID: "targets", Source: "example", Reason: "target allowlist"}}
	if req.Resource.ID == "internal-writer" {
		return policy.Decision{Effect: policy.Allow, Reason: "internal target", MatchedPolicies: match}, nil
	}
	return policy.Decision{Effect: policy.Ask, Reason: "external target", MatchedPolicies: match}, nil
}

type discard struct{}

func (discard) Record(context.Context, policy.AuditEvent) error { return nil }

func Example() {
	set, _ := router.ParseRuleSet([]byte(`{"version":"1","strategy":"fan_out","rules":[
	  {"id":"internal","when":[{"field":"kind","op":"eq","value":"note"}],"target":"internal-writer","auto":true},
	  {"id":"external","when":[{"field":"kind","op":"eq","value":"note"}],"target":"external-webhook","auto":true}]}`))

	r, _ := router.Compile(set, router.Config{Policy: routepolicy.Adapter{
		Evaluator: policy.Evaluator{PDP: askForExternal{}, Audit: discard{}},
		Options:   policy.Options{Mode: policy.Enforce, FailMode: policy.FailClosed},
		Map:       routepolicy.DefaultMapper("example/routing"),
	}})

	d, _ := r.Route(context.Background(), router.Subject{ID: "msg-1", Fields: map[string]string{
		"kind": "note", "principal.urn": "urn:example:agent:triage", "principal.kind": "agent",
	}})
	for _, m := range d.Matches {
		fmt.Println(m.Target, m.Disposition)
	}
	// Output:
	// internal-writer apply
	// external-webhook ask
}
