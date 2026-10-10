package policy_test

import (
	"testing"
	"time"

	"github.com/hollis-labs/substrate/policy"
)

func request() policy.Request {
	return policy.Request{RequestID: "request-1", Principal: policy.Principal{URN: "urn:example:agent", Kind: "agent"},
		Action: "file.write", Resource: policy.Resource{Type: "file", ID: "workspace/file"}, Scope: "workspace"}
}

func TestRequestShapeDoesNotAuthenticateIdentityOrValidatePolicyFacts(t *testing.T) {
	r := request()
	r.Context = map[string]any{"budget_state": "host-supplied", "arbitrary_fact": true}
	r.Principal.OnBehalfOf = "urn:example:owner"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*policy.Request){
		"missing identity":           func(r *policy.Request) { r.Principal.URN = "" },
		"label instead of reference": func(r *policy.Request) { r.Principal.URN = "architect" },
		"invalid delegation":         func(r *policy.Request) { r.Principal.OnBehalfOf = "owner" },
		"missing action":             func(r *policy.Request) { r.Action = "" },
		"missing resource":           func(r *policy.Request) { r.Resource.ID = "" },
		"missing scope":              func(r *policy.Request) { r.Scope = "" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := r
			mutate(&invalid)
			if invalid.Validate() == nil {
				t.Fatal("invalid contract shape admitted")
			}
		})
	}
}

func obligations() []policy.Obligation {
	return []policy.Obligation{
		{Kind: policy.Approval, PolicyID: "approval-rule", Reference: "host-bound-request"},
		{Kind: policy.DryRunOnly, PolicyID: "preview-rule"},
		{Kind: policy.RateLimit, PolicyID: "rate-rule", Reference: "host-rate-key", Limit: &policy.Limit{Count: 2, Window: time.Hour, Unit: "operations"}},
	}
}

func TestDecisionRetainsMandatoryObligationsAndRejectsUnsupportedIntent(t *testing.T) {
	d := policy.Decision{Effect: policy.Ask, MatchedPolicies: []policy.PolicyMatch{{ID: "approval-rule", Source: "host"}}, Obligations: obligations()}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []policy.Obligation{
		{Kind: "future_optional"},
		{Kind: policy.Approval, PolicyID: "approval-rule"},
		{Kind: policy.Approval, PolicyID: "approval-rule", Reference: "request", Limit: &policy.Limit{Count: 2, Window: time.Hour, Unit: "ops"}},
		{Kind: policy.DryRunOnly, PolicyID: "preview-rule", Limit: &policy.Limit{}},
		{Kind: policy.RateLimit, PolicyID: "rate-rule", Reference: "rate-key", Limit: &policy.Limit{Count: 0, Window: time.Hour, Unit: "ops"}},
		{Kind: policy.RateLimit, PolicyID: "rate-rule", Reference: "rate-key", Limit: &policy.Limit{Count: 1, Window: 0, Unit: "ops"}},
	} {
		invalid := d
		invalid.Obligations = []policy.Obligation{bad}
		if invalid.Validate() == nil {
			t.Fatalf("unsupported intent admitted: %q", bad.Kind)
		}
	}
	for _, effect := range []policy.Effect{"", "approve", "dry_run_only", "future"} {
		if (policy.Decision{Effect: effect}).Validate() == nil {
			t.Fatalf("host decision vocabulary implicitly translated: %q", effect)
		}
	}
}
