package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

func TestBackgroundOnlyForPluginActor(t *testing.T) {
	for _, kind := range []SubjectKind{UserActor, AgentClient, SessionClient, MCPProxyClient} {
		t.Run(string(kind), func(t *testing.T) {
			e, a, c, _, releases, events := enforcementFixture(t)
			a.Actor = Subject{kind, "client"}
			err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("non-plugin background effect"); return nil })
			failureCode(t, err, capability.InvalidRequest)
			if releases.Load() != 0 || len(*events) != 1 || (*events)[0].Outcome != capability.InvalidRequest {
				t.Fatal("background refusal lost")
			}
		})
	}
}

func TestBackgroundWithCallerWithoutPolicyIsTypedRefusal(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	a.InitiatingCaller = &Subject{UserActor, "user"}
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("ambiguous background effect"); return nil })
	failureCode(t, err, capability.InvalidRequest)
}

// These changes occur after RequireCapability's final check. Only Commit's
// own fresh check can prevent the callback from running.
func TestCommitRechecksCurrentAuthorityBeforeEffect(t *testing.T) {
	for _, change := range []string{"revision", "generation", "lease", "policy", "grant audience"} {
		t.Run(change, func(t *testing.T) {
			e, a, c, cancel, releases, _ := enforcementFixture(t)
			p, err := e.RequireCapability(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			want := capability.CapabilityDenied
			switch change {
			case "revision":
				a.PolicyRevision = "withdrawn"
			case "generation":
				a.Owner.OwnerGeneration++
				a.Grant.OwnerGeneration++
				want = capability.TargetUnavailable
			case "lease":
				cancel()
			case "policy":
				a.Policy.Allowlists["keys"] = nil
				want = capability.ScopeDenied
			case "grant audience":
				a.Grant.Audience = "other"
				want = capability.Unauthenticated
			}
			effects := 0
			err = p.Commit(func(context.Context) error { effects++; return nil })
			failureCode(t, err, want)
			var failure *capability.Error
			errors.As(err, &failure)
			if failure.EffectState != capability.NotStarted || effects != 0 || releases.Load() != 1 || p.Context.Err() == nil {
				t.Fatal("commit admitted withdrawn authority or leaked reservation")
			}
			if change == "generation" && failure.Detail != capability.StaleBinding {
				t.Fatal("generation refusal lost stale binding")
			}
		})
	}
}

func TestGrantAudienceCheckedIndependently(t *testing.T) {
	e, a, c, _, releases, _ := enforcementFixture(t)
	a.Grant.Audience = "another-audience"
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("grant audience bypass"); return nil }), capability.Unauthenticated)
	if releases.Load() != 0 {
		t.Fatal("wrong grant audience reserved budget")
	}
}

func TestWrongOwnerIsDeniedWithoutStaleDetail(t *testing.T) {
	for _, stage := range []string{"admission", "recheck"} {
		t.Run(stage, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			var err error
			if stage == "admission" {
				a.Grant.OwnerID = "other"
				_, err = e.RequireCapability(context.Background(), c)
			} else {
				p, failure := e.RequireCapability(context.Background(), c)
				if failure != nil {
					t.Fatal(failure)
				}
				defer p.Close()
				a.Owner.OwnerID = "other"
				a.Actor.ID = "other"
				a.Grant.OwnerID = "other"
				err = p.Recheck()
			}
			failureCode(t, err, capability.CapabilityDenied)
			var failure *capability.Error
			errors.As(err, &failure)
			if failure.Detail != "" || failure.EffectState != capability.NotStarted {
				t.Fatal("wrong owner claimed stale binding")
			}
		})
	}
}

type failingNowClock struct {
	Clock
	fail bool
}

func (c *failingNowClock) Now() time.Time {
	if c.fail {
		panic("clock unavailable")
	}
	return c.Clock.Now()
}

func TestDirectRecheckContainsPanicAndClosesPermit(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	clock := &failingNowClock{Clock: e.Clock}
	e.Clock = clock
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	clock.fail = true
	failureCode(t, p.Recheck(), capability.InternalError)
	if !p.closed.Load() || p.Context.Err() == nil || releases.Load() != 1 {
		t.Fatal("recheck panic did not close and release")
	}
}

func TestAdmissionNormalizesResolverAndBudgetErrors(t *testing.T) {
	for _, source := range []string{"resolver", "budget"} {
		for _, code := range []capability.Code{capability.Conflict, capability.UnknownOutcome} {
			t.Run(source+"/"+string(code), func(t *testing.T) {
				e, _, c, _, releases, events := enforcementFixture(t)
				effect := capability.Committed
				want := code
				if code == capability.UnknownOutcome {
					effect = capability.Unknown
					want = capability.InternalError
				}
				claimed := &capability.Error{Code: code, EffectState: effect}
				if source == "resolver" {
					e.Resolver = authorityFunc(func(context.Context, Call) (Authority, error) { return Authority{}, claimed })
				} else {
					e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) { return func() { releases.Add(1) }, claimed })
				}
				err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("callback after admission error"); return nil })
				failureCode(t, err, want)
				var failure *capability.Error
				errors.As(err, &failure)
				if failure.EffectState != capability.NotStarted || failure.Capability != c.Capability || failure.RequestID != c.RequestID {
					t.Fatal("admission inherited callback outcome/metadata")
				}
				if len(*events) != 1 || (*events)[0].EffectState != capability.NotStarted || (*events)[0].Outcome != want {
					t.Fatal("normalized denial audit missing")
				}
				if source == "budget" && releases.Load() != 1 {
					t.Fatal("error with partial reservation leaked")
				}
			})
		}
	}
}

func TestRecheckStillRejectsOriginalLeaseAfterResolverReplacesIt(t *testing.T) {
	e, a, c, cancel, releases, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Hold watcher delivery to exercise the synchronous old-lease check rather
	// than relying on eventual merged-context cancellation.
	if !p.stopAuthority() {
		t.Fatal("watcher already fired")
	}
	a.LeaseContext = context.Background()
	cancel()
	if p.Context.Err() != nil {
		t.Fatal("watcher was not held")
	}
	failureCode(t, p.Recheck(), capability.CapabilityDenied)
	p.Close()
	if releases.Load() != 1 {
		t.Fatal("old lease refusal leaked budget")
	}
}

func TestAuditTimestampPanicDoesNotEraseOutcome(t *testing.T) {
	e, a, c, _, _, events := enforcementFixture(t)
	e.Clock = &failingNowClock{Clock: e.Clock, fail: true}
	e.record(context.Background(), c, *a, time.Now(), nil, capability.Committed)
	if len(*events) != 1 || (*events)[0].Timestamp.IsZero() || (*events)[0].EffectState != capability.Committed || e.Counters().Allowed != 1 || e.Audit.Failures() != 1 {
		t.Fatal("timestamp failure erased outcome or failure counter")
	}
}

func TestEnforcerConstructionAndUnconfiguredUseFailClosed(t *testing.T) {
	for _, field := range []string{"host", "audience", "catalog", "resolver", "budget"} {
		t.Run(field, func(t *testing.T) {
			e, _, _, _, _, _ := enforcementFixture(t)
			cfg := EnforcerConfig{HostInstance: "epoch", Audience: "stdio", Catalog: e.Catalog, Resolver: e.Resolver, Budget: e.Budget}
			switch field {
			case "host":
				cfg.HostInstance = "mid\ncontrol"
			case "audience":
				cfg.Audience = " padded"
			case "catalog":
				cfg.Catalog = nil
			case "resolver":
				cfg.Resolver = nil
			case "budget":
				cfg.Budget = nil
			}
			constructed, err := NewEnforcer(cfg)
			failureCode(t, err, capability.InvalidRequest)
			if constructed != nil {
				t.Fatal("invalid configuration constructed enforcer")
			}
		})
	}
	for _, field := range []string{"host", "audience"} {
		t.Run("unconfigured/"+field, func(t *testing.T) {
			e, _, c, _, releases, _ := enforcementFixture(t)
			if field == "host" {
				e.host = ""
			} else {
				e.audience = ""
			}
			failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("unconfigured effect"); return nil }), capability.InternalError)
			if releases.Load() != 0 {
				t.Fatal("unconfigured reservation")
			}
		})
	}
}

func TestAuditSanitizesEveryMetadataField(t *testing.T) {
	bad := "mid\u202Eevil"
	event := AuditEvent{TraceID: bad, RequestID: bad, Actor: Actor{bad, bad}, InitiatingCaller: &Actor{bad, bad}, Owner: capability.RuntimeIdentity{HostInstance: bad, OwnerID: bad, OwnerGeneration: capability.MaxSafeInteger + 1}, Capability: bad, GrantID: bad, PolicyRevision: bad, Target: bad, Server: bad, Tool: bad, Effect: capability.Effect(bad)}
	var got AuditEvent
	auditor := NewAuditor(auditFunc(func(_ context.Context, e AuditEvent) error { got = e; return nil }))
	if !auditor.Record(context.Background(), event) {
		t.Fatal("sanitization denied telemetry")
	}
	for _, value := range []string{got.TraceID, got.RequestID, got.Actor.Kind, got.Actor.ID, got.InitiatingCaller.Kind, got.InitiatingCaller.ID, got.Owner.HostInstance, got.Owner.OwnerID, got.Capability, got.GrantID, got.PolicyRevision, got.Target, got.Server, got.Tool} {
		if value != invalidAuditIdentifier {
			t.Fatalf("unsafe audit field %q", value)
		}
	}
	if got.Owner.OwnerGeneration != 0 || got.Effect != "" {
		t.Fatal("unsafe generation/effect survived")
	}
	if event.InitiatingCaller.Kind != bad || event.Actor.ID != bad {
		t.Fatal("sanitization mutated caller-owned metadata")
	}
}

func TestAuditScrubbingIsUnicodeSafeAndUnambiguous(t *testing.T) {
	for _, value := range []string{"a\u202Eb", "a\u200Bb", "a\u200Eb", "a\u2066b", "a\u061Cb", "a\x00b", "a\nb"} {
		if auditIdentifier(value) != invalidAuditIdentifier {
			t.Fatalf("unsafe Unicode/control identifier %q", value)
		}
	}
	if identifier(invalidAuditIdentifier) || auditIdentifier("[invalid]") != "[invalid]" || auditIdentifier("target") != "target" {
		t.Fatal("scrub marker aliases valid identifier")
	}
}

func TestPlanningResolverCannotMutateOriginalRequest(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	saved := cloneScope(request.Scope)
	resolver := scopeFunc(func(_ context.Context, r Request) (ScopeInputs, error) {
		r.Scope.Allowlists["keys"][0] = "different"
		r.Scope.Limits["response_bytes"] = 1
		return ScopeInputs{cloneScope(saved), cloneScope(saved), cloneScope(saved)}, nil
	})
	grants, notices, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
	if err != nil || len(grants) != 1 || len(notices) != 0 {
		t.Fatalf("resolver mutation changed original planning input: %v", err)
	}
	var scope capability.Scope
	if err := json.Unmarshal(grants[0].Scope, &scope); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request.Scope, saved) || !reflect.DeepEqual(scope, saved) {
		t.Fatal("callback mutated request or resulting grant")
	}
}

func TestPlanningUnknownOutcomeCannotBecomeOptionalRefusal(t *testing.T) {
	catalog, request, plan := planningFixture(t)
	request.Optional = true
	resolver := scopeFunc(func(context.Context, Request) (ScopeInputs, error) {
		return ScopeInputs{}, &capability.Error{Code: capability.UnknownOutcome, EffectState: capability.Unknown}
	})
	grants, notices, err := ResolveGrants(context.Background(), catalog, resolver, []Request{request}, plan)
	failureCode(t, err, capability.InternalError)
	var failure *capability.Error
	errors.As(err, &failure)
	if failure.EffectState != capability.NotStarted || grants != nil || len(notices) != 0 {
		t.Fatal("planning implementation failure became optional denial")
	}
}
