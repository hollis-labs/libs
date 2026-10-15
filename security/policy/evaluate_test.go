package policy_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/policy"
)

type pdpFunc func(context.Context, policy.Request) (policy.Decision, error)

func (f pdpFunc) Evaluate(ctx context.Context, r policy.Request) (policy.Decision, error) {
	return f(ctx, r)
}

type auditFunc func(context.Context, policy.AuditEvent) error

func (f auditFunc) Record(ctx context.Context, e policy.AuditEvent) error { return f(ctx, e) }

func TestEvaluationPreservesPolicyIntentInEnforceAndShadow(t *testing.T) {
	for _, mode := range []policy.Mode{policy.Enforce, policy.Shadow} {
		for _, effect := range []policy.Effect{policy.Allow, policy.Ask, policy.Deny} {
			t.Run(string(mode)+"/"+string(effect), func(t *testing.T) {
				want := policy.Decision{Effect: effect, Reason: "host policy reason",
					MatchedPolicies: []policy.PolicyMatch{{ID: "baseline", Source: "baseline"}, {ID: "target", Source: "target", Reason: "target reason"}}, Obligations: obligations()}
				calls, events := 0, 0
				var recorded policy.AuditEvent
				e := policy.Evaluator{PDP: pdpFunc(func(_ context.Context, r policy.Request) (policy.Decision, error) {
					calls++
					if r.Principal.URN != request().Principal.URN || r.Context["budget_state"] != "host-owned" {
						t.Fatal("host facts or verified identity discarded")
					}
					return want, nil
				}), Audit: auditFunc(func(_ context.Context, event policy.AuditEvent) error {
					events++
					recorded = event
					return nil
				})}
				r := request()
				r.Context = map[string]any{"budget_state": "host-owned", "tool_input": "private fixture payload"}
				got, err := e.Evaluate(context.Background(), r, policy.Options{Mode: mode, FailMode: policy.FailClosed})
				admission := effect
				if mode == policy.Shadow {
					admission = policy.Allow
				}
				if err != nil || got.Admission != admission || got.WouldBlock != (effect != policy.Allow) || !got.AuditRecorded || calls != 1 || events != 1 {
					t.Fatalf("incorrect admission/audit: %+v err=%v calls=%d events=%d", got, err, calls, events)
				}
				if !reflect.DeepEqual(got.Decision, want) || !reflect.DeepEqual(recorded.Decision, want) {
					t.Fatal("decision provenance or mandatory obligations changed")
				}
				encoded, err := json.Marshal(recorded)
				if err != nil || strings.Contains(string(encoded), "private fixture payload") || strings.Contains(string(encoded), "tool_input") {
					t.Fatal("raw request facts entered audit", err)
				}
				got.Decision.Obligations[2].Limit.Count = 99
				got.Decision.MatchedPolicies[0].ID = "mutated-result"
				if want.Obligations[2].Limit.Count != 2 || recorded.Decision.Obligations[2].Limit.Count != 2 || recorded.Decision.MatchedPolicies[0].ID != "baseline" {
					t.Fatal("PDP, result and audit share mutable policy intent")
				}
			})
		}
	}
}

func TestPerCallFailureModesRequireSuccessfulAuditAndKeepErrorsClassified(t *testing.T) {
	privateError := errors.New("private evaluator fixture payload")
	for _, mode := range []policy.Mode{policy.Enforce, policy.Shadow} {
		for _, failMode := range []policy.FailMode{policy.FailClosed, policy.FailOpenWithAudit} {
			for _, scenario := range []string{"pdp error", "no pdp", "invalid decision", "audit error", "no audit"} {
				t.Run(string(mode)+"/"+string(failMode)+"/"+scenario, func(t *testing.T) {
					events := 0
					pdp := pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) {
						if scenario == "pdp error" {
							return policy.Decision{Effect: policy.Allow, Obligations: obligations()}, privateError
						}
						if scenario == "invalid decision" {
							return policy.Decision{Effect: policy.Allow, Obligations: []policy.Obligation{{Kind: "unsupported"}}}, nil
						}
						return policy.Decision{Effect: policy.Allow}, nil
					})
					e := policy.Evaluator{PDP: pdp, Audit: auditFunc(func(_ context.Context, event policy.AuditEvent) error {
						events++
						encoded, marshalErr := json.Marshal(event)
						if marshalErr != nil || strings.Contains(string(encoded), privateError.Error()) {
							t.Fatal("exception text entered audit")
						}
						if scenario == "audit error" {
							return privateError
						}
						return nil
					})}
					if scenario == "no pdp" {
						e.PDP = nil
					}
					if scenario == "no audit" {
						e.Audit = nil
					}
					got, err := e.Evaluate(context.Background(), request(), policy.Options{Mode: mode, FailMode: failMode})
					var classified *policy.Error
					if !errors.As(err, &classified) || classified.Code != got.Failure || strings.Contains(err.Error(), privateError.Error()) {
						t.Fatalf("unclassified/private failure: %v", err)
					}
					want := policy.Deny
					if failMode == policy.FailOpenWithAudit && scenario != "audit error" && scenario != "no audit" {
						want = policy.Allow
					}
					if got.Admission != want {
						t.Fatalf("admission=%s want=%s", got.Admission, want)
					}
					if scenario == "audit error" || scenario == "no audit" {
						if got.AuditRecorded || !errors.Is(err, policy.ErrAuditUnavailable) {
							t.Fatal("audit failure falsely admitted or recorded")
						}
						if scenario == "no audit" && events != 0 {
							t.Fatal("missing sink used")
						}
					} else if !got.AuditRecorded || got.Decision.Effect != policy.Deny {
						t.Fatal("failed/partial evaluation became a valid policy allow")
					}
					if scenario != "no audit" && events != 1 {
						t.Fatal("audit retried or omitted")
					}
				})
			}
		}
	}
}

func TestInvalidCallsAndCancellationNeverFailOpenOrInvokePDP(t *testing.T) {
	for _, scenario := range []string{"options", "request", "canceled", "nil context"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			opts := policy.Options{Mode: policy.Shadow, FailMode: policy.FailOpenWithAudit}
			r := request()
			switch scenario {
			case "options":
				opts.Mode = ""
			case "request":
				r.Principal.URN = "self-claimed label"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			}
			calls, events := 0, 0
			e := policy.Evaluator{PDP: pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) {
				calls++
				return policy.Decision{Effect: policy.Allow}, nil
			}),
				Audit: auditFunc(func(context.Context, policy.AuditEvent) error { events++; return nil })}
			got, err := e.Evaluate(ctx, r, opts)
			if err == nil || got.Admission != policy.Deny || calls != 0 || events != 1 {
				t.Fatalf("invalid/canceled call admitted: %+v err=%v", got, err)
			}
		})
	}
}

func TestCancellationDuringPDPRefusesDespiteAllowAndOpenMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := policy.Evaluator{PDP: pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) {
		cancel()
		return policy.Decision{Effect: policy.Allow}, nil
	}),
		Audit: auditFunc(func(context.Context, policy.AuditEvent) error { return nil })}
	got, err := e.Evaluate(ctx, request(), policy.Options{Mode: policy.Shadow, FailMode: policy.FailOpenWithAudit})
	if got.Admission != policy.Deny || got.Failure != policy.Canceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %+v %v", got, err)
	}
}

func TestAuditMutationCannotFulfillOrChangeReturnedObligations(t *testing.T) {
	want := policy.Decision{Effect: policy.Ask, Obligations: obligations()}
	e := policy.Evaluator{PDP: pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) { return want, nil }),
		Audit: auditFunc(func(_ context.Context, event policy.AuditEvent) error {
			event.Decision.Obligations[2].Limit.Count = 99
			return nil
		})}
	got, err := e.Evaluate(context.Background(), request(), policy.Options{Mode: policy.Enforce, FailMode: policy.FailClosed})
	if err != nil || got.Admission != policy.Ask || !reflect.DeepEqual(got.Decision, want) {
		t.Fatal("audit changed policy admission", err)
	}
}

func TestAuditFailureRetainsIntentButNeverClaimsFinalAdmissionOrDurability(t *testing.T) {
	var proposed policy.AuditEvent
	e := policy.Evaluator{PDP: pdpFunc(func(context.Context, policy.Request) (policy.Decision, error) {
		return policy.Decision{Effect: policy.Allow, Obligations: obligations()}, nil
	}), Audit: auditFunc(func(_ context.Context, event policy.AuditEvent) error {
		proposed = event
		return errors.New("sink accepted an attempt but cannot confirm durability")
	})}
	got, err := e.Evaluate(context.Background(), request(), policy.Options{Mode: policy.Shadow, FailMode: policy.FailOpenWithAudit})
	if proposed.ProposedAdmission != policy.Allow || got.Admission != policy.Deny || got.AuditRecorded || !errors.Is(err, policy.ErrAuditUnavailable) {
		t.Fatal("audit attempt confused with durable admission", got, err)
	}
	if got.Decision.Effect != policy.Allow || !reflect.DeepEqual(got.Decision.Obligations, obligations()) {
		t.Fatal("audit failure destroyed evaluated intent")
	}
}
