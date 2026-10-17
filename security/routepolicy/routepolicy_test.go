package routepolicy_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/router"
	"github.com/hollis-labs/libs/security/policy"
	"github.com/hollis-labs/libs/security/routepolicy"
)

type pdp func(policy.Request) (policy.Decision, error)

func (p pdp) Evaluate(_ context.Context, r policy.Request) (policy.Decision, error) { return p(r) }

type sink struct {
	events []policy.AuditEvent
	err    error
}

func (s *sink) Record(_ context.Context, e policy.AuditEvent) error {
	s.events = append(s.events, e)
	return s.err
}

const rules = `{"version":"v1","strategy":"fan_out","rules":[
 {"id":"gh","when":[{"field":"source","op":"eq","value":"inbox"}],"target":"github-enricher","auto":true}]}`

func subject() router.Subject {
	return router.Subject{ID: "req-1", Fields: map[string]string{
		"source": "inbox", "principal.urn": "urn:hollis:agent:triage", "principal.kind": "agent",
		"entity.value": "https://example.com/private?token=SECRET-IN-INPUT",
	}}
}

func setup(t *testing.T, p pdp, opts policy.Options, audit *sink) *router.Router {
	t.Helper()
	rs, err := router.ParseRuleSet([]byte(rules))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.Compile(rs, router.Config{Policy: routepolicy.Adapter{
		Evaluator: policy.Evaluator{PDP: p, Audit: audit},
		Options:   opts,
		Map:       routepolicy.DefaultMapper("route/app"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var enforce = policy.Options{Mode: policy.Enforce, FailMode: policy.FailClosed}

func allow(policy.Request) (policy.Decision, error) {
	return policy.Decision{Effect: policy.Allow, Reason: "ok", MatchedPolicies: []policy.PolicyMatch{{ID: "p1", Source: "yaml", Reason: "trusted source"}}}, nil
}

func TestEffectsMapThroughRouter(t *testing.T) {
	for _, c := range []struct {
		name string
		eff  policy.Effect
		want router.Outcome
	}{
		{"allow", policy.Allow, router.Routed},
		{"ask", policy.Ask, router.Suggested},
		{"deny", policy.Deny, router.Denied},
	} {
		a := &sink{}
		r := setup(t, func(policy.Request) (policy.Decision, error) {
			return policy.Decision{Effect: c.eff, Reason: "r", MatchedPolicies: []policy.PolicyMatch{{ID: "p1"}}}, nil
		}, enforce, a)
		d, _ := r.Route(context.Background(), subject())
		if d.Outcome != c.want || len(a.events) != 1 {
			t.Errorf("%s: %+v audit=%d", c.name, d, len(a.events))
		}
		if len(d.Matches) != 1 || len(d.Matches[0].Policies) != 1 || d.Matches[0].Policies[0].ID != "p1" {
			t.Errorf("%s: provenance lost: %+v", c.name, d.Matches)
		}
	}
}

func TestRequestShape(t *testing.T) {
	var got policy.Request
	r := setup(t, func(req policy.Request) (policy.Decision, error) { got = req; return allow(req) }, enforce, &sink{})
	r.Route(context.Background(), subject())
	if got.Action != "route:github-enricher" || got.Resource.ID != "github-enricher" || got.Scope != "route/app" ||
		got.Principal.URN != "urn:hollis:agent:triage" || got.RequestID != "req-1" {
		t.Fatalf("%+v", got)
	}
	if got.Context["rule_id"] != "gh" {
		t.Fatalf("%+v", got.Context)
	}
}

func TestObligationsFlowToRouterRules(t *testing.T) {
	r := setup(t, func(policy.Request) (policy.Decision, error) {
		return policy.Decision{Effect: policy.Allow, Reason: "ok", MatchedPolicies: []policy.PolicyMatch{{ID: "p1"}},
			Obligations: []policy.Obligation{
				{Kind: policy.Approval, PolicyID: "p1", Reference: "ap-9"},
				{Kind: policy.RateLimit, PolicyID: "p1", Reference: "rl", Limit: &policy.Limit{Count: 2, Window: time.Hour, Unit: "route"}},
			}}, nil
	}, enforce, &sink{})
	d, _ := r.Route(context.Background(), subject())
	if d.Outcome != router.Suggested || d.Matches[0].Disposition != router.Ask {
		t.Fatalf("allow+approval must ask: %+v", d.Matches)
	}
	if len(d.Matches[0].Obligations) != 2 || d.Matches[0].Obligations[1].Limit.Count != 2 {
		t.Fatalf("obligations lost: %+v", d.Matches[0].Obligations)
	}
}

func TestPolicyShadowModeAppliesButRecordsWouldBlock(t *testing.T) {
	a := &sink{}
	r := setup(t, func(policy.Request) (policy.Decision, error) {
		return policy.Decision{Effect: policy.Deny, Reason: "would deny", MatchedPolicies: []policy.PolicyMatch{{ID: "p1"}}}, nil
	}, policy.Options{Mode: policy.Shadow, FailMode: policy.FailClosed}, a)
	d, _ := r.Route(context.Background(), subject())
	if d.Outcome != router.Routed {
		t.Fatalf("evaluator shadow admits: %+v", d)
	}
	if len(a.events) != 1 || !a.events[0].WouldBlock {
		t.Fatalf("audit must record would-block: %+v", a.events)
	}
}

func TestFailuresFailClosedWithoutLeakingText(t *testing.T) {
	boom := func(policy.Request) (policy.Decision, error) {
		return policy.Decision{}, errors.New("db down, PDP-ERROR-TEXT")
	}
	r := setup(t, boom, enforce, &sink{})
	d, _ := r.Route(context.Background(), subject())
	if d.Outcome != router.Denied || d.Matches[0].PolicyFailure != string(policy.PDPFailed) {
		t.Fatalf("%+v", d.Matches)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "PDP-ERROR-TEXT") {
		t.Fatalf("policy error text reached the decision row: %s", b)
	}

	// fail-open only with a recorded audit
	a := &sink{}
	r = setup(t, boom, policy.Options{Mode: policy.Enforce, FailMode: policy.FailOpenWithAudit}, a)
	d, _ = r.Route(context.Background(), subject())
	if d.Outcome != router.Routed || d.Matches[0].PolicyFailure != string(policy.PDPFailed) {
		t.Fatalf("fail-open with audit: %+v", d.Matches)
	}
	a2 := &sink{err: errors.New("disk full")}
	r = setup(t, boom, policy.Options{Mode: policy.Enforce, FailMode: policy.FailOpenWithAudit}, a2)
	if d, _ = r.Route(context.Background(), subject()); d.Outcome != router.Denied {
		t.Fatalf("audit failure must tighten to deny: %+v", d.Matches)
	}
}

func TestAuditUnavailableDenies(t *testing.T) {
	r := setup(t, allow, enforce, &sink{err: errors.New("down")})
	if d, _ := r.Route(context.Background(), subject()); d.Outcome != router.Denied {
		t.Fatalf("%+v", d)
	}
}

func TestMappingFailureAndInvalidRequestDeny(t *testing.T) {
	r := setup(t, allow, enforce, &sink{})
	s := subject()
	delete(s.Fields, "principal.urn")
	if d, _ := r.Route(context.Background(), s); d.Outcome != router.Denied {
		t.Fatalf("no principal must deny: %+v", d)
	}
	s = subject()
	s.Fields["principal.urn"] = "not a urn"
	d, _ := r.Route(context.Background(), s)
	if d.Outcome != router.Denied || d.Matches[0].PolicyFailure != string(policy.InvalidRequest) {
		t.Fatalf("invalid request: %+v", d.Matches)
	}
}

func TestUnknownObligationKindDenies(t *testing.T) {
	// the evaluator itself rejects it as an invalid decision
	r := setup(t, func(policy.Request) (policy.Decision, error) {
		return policy.Decision{Effect: policy.Allow, Obligations: []policy.Obligation{{Kind: "teleport", PolicyID: "p"}}}, nil
	}, enforce, &sink{})
	d, _ := r.Route(context.Background(), subject())
	if d.Outcome != router.Denied || d.Matches[0].PolicyFailure != string(policy.InvalidDecision) {
		t.Fatalf("%+v", d.Matches)
	}
}
