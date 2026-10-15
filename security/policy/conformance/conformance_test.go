package conformance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/policy"
	"github.com/hollis-labs/substrate/policy/conformance"
)

type pdpFunc func(context.Context, policy.Request) (policy.Decision, error)

func (f pdpFunc) Evaluate(ctx context.Context, r policy.Request) (policy.Decision, error) {
	return f(ctx, r)
}

// These are neutral projections of recorded cases, not implementations of the
// source engines. The scripted PDP below tests the conformance checker itself.
// Provenance: substrate c9685349, harness/interception/permission/engine_test.go
// default non-destructive-write allow and destructive-write ask; Cerberus
// a28f975d, internal/policy/policy_test.go combining/dry-run cases and rate tests.
func recordedCases() []conformance.Case {
	r := policy.Request{Principal: policy.Principal{URN: "urn:example:agent", Kind: "agent"}, Resource: policy.Resource{Type: "file", ID: "workspace/file"}, Scope: "workspace"}
	makeCase := func(name, action string, expected policy.Decision) conformance.Case {
		request := r
		request.Action = action
		return conformance.Case{Name: name, Request: request, Expected: expected}
	}
	return []conformance.Case{
		makeCase("permission/default non-destructive write", "file.write", policy.Decision{Effect: policy.Allow}),
		makeCase("permission/default destructive write", "file.delete", policy.Decision{Effect: policy.Ask}),
		makeCase("cerberus/deny still denies preview", "protected.preview", policy.Decision{Effect: policy.Deny, Reason: "host deny", MatchedPolicies: []policy.PolicyMatch{{ID: "baseline", Source: "baseline"}, {ID: "target", Source: "target"}}}),
		makeCase("cerberus/approval carries host binding", "controlled.write", policy.Decision{Effect: policy.Ask, Obligations: []policy.Obligation{{Kind: policy.Approval, PolicyID: "controlled", Reference: "host-binding"}}}),
		makeCase("cerberus/preview-only intent", "preview.operation", policy.Decision{Effect: policy.Allow, Obligations: []policy.Obligation{{Kind: policy.DryRunOnly, PolicyID: "preview"}}}),
		makeCase("cerberus/two-per-hour host limit", "limited.operation", policy.Decision{Effect: policy.Allow, Obligations: []policy.Obligation{{Kind: policy.RateLimit, PolicyID: "limited", Reference: "host-rate-key", Limit: &policy.Limit{Count: 2, Window: time.Hour, Unit: "operations"}}}}),
	}
}

func TestCheckerAcceptsFullDecisionsAndDetectsDroppedHostIntent(t *testing.T) {
	for _, change := range []string{"none", "effect", "reason", "policy order", "obligations", "error", "invalid"} {
		t.Run(change, func(t *testing.T) {
			cases := recordedCases()
			pdp := pdpFunc(func(_ context.Context, r policy.Request) (policy.Decision, error) {
				for _, c := range recordedCases() {
					if c.Request.Action != r.Action {
						continue
					}
					got := c.Expected
					switch change {
					case "effect":
						got.Effect = policy.Deny
					case "reason":
						got.Reason = "changed host reason"
					case "policy order":
						if len(got.MatchedPolicies) == 2 {
							got.MatchedPolicies[0], got.MatchedPolicies[1] = got.MatchedPolicies[1], got.MatchedPolicies[0]
						}
					case "obligations":
						got.Obligations = nil
					case "error":
						return got, errors.New("private host fixture input")
					case "invalid":
						got.Effect = "approve"
					}
					return got, nil
				}
				return policy.Decision{}, errors.New("fixture unavailable")
			})
			mismatches, err := conformance.Check(context.Background(), pdp, cases)
			if err != nil {
				t.Fatal(err)
			}
			if change == "none" && len(mismatches) != 0 {
				t.Fatal("correct fixture rejected", mismatches)
			}
			if change != "none" && len(mismatches) == 0 {
				t.Fatal("checker failed to detect lost behavior")
			}
			for _, mismatch := range mismatches {
				if mismatch.Name == "" || mismatch.Reason == "private host fixture input" {
					t.Fatal("missing case identity or raw exception output")
				}
			}
		})
	}
}

func TestEmptyDuplicateOrInvalidCorporaCannotClaimConformance(t *testing.T) {
	calls := 0
	pdp := pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) {
		calls++
		return policy.Decision{Effect: policy.Allow}, nil
	})
	valid := recordedCases()
	invalid := valid[0]
	invalid.Expected.Effect = ""
	for _, corpus := range [][]conformance.Case{nil, {valid[0], valid[0]}, {invalid}} {
		if _, err := conformance.Check(context.Background(), pdp, corpus); err == nil {
			t.Fatal("invalid corpus passed")
		}
	}
	if calls != 0 {
		t.Fatal("invalid corpus reached PDP")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := conformance.Check(ctx, pdp, valid); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal("canceled conformance reached PDP")
	}
}
