package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

type authorityFunc func(context.Context, Call) (Authority, error)

func (f authorityFunc) Resolve(ctx context.Context, call Call) (Authority, error) {
	return f(ctx, call)
}

type budgetFunc func(context.Context, Authority, Call) (func(), error)

func (f budgetFunc) Reserve(ctx context.Context, a Authority, c Call) (func(), error) {
	return f(ctx, a, c)
}
func enforcementFixture(t *testing.T) (*Enforcer, *Authority, Call, context.CancelFunc, *atomic.Int64, *[]AuditEvent) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	scope := capability.Scope{Allowlists: map[string][]string{"operations": {"host/storage/put"}, "targets": {"owner"}, "effects": {"write"}, "keys": {"preferences"}}, Limits: map[string]int64{"request_bytes": 100}}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tuple := capability.RuntimeIdentity{HostInstance: "epoch", OwnerID: "owner", OwnerGeneration: 1}
	grant := capability.Grant{GrantID: "grant", Name: capability.StorageWrite, SchemaVersion: 1, Scope: raw, HostInstance: tuple.HostInstance, OwnerID: tuple.OwnerID, OwnerGeneration: tuple.OwnerGeneration, Audience: "stdio", IssuedAt: clock.Now().Format(time.RFC3339Nano), ExpiresAt: clock.Now().Add(time.Minute).Format(time.RFC3339Nano), PolicyRevision: "reviewed"}
	auth := &Authority{Background: true, Authenticated: true, Actor: Subject{PluginActor, "owner"}, Owner: tuple, Audience: "stdio", Grant: grant, PolicyRevision: "reviewed", Policy: cloneScope(scope), TransportScope: &scope, LeaseContext: life, Active: true, TargetAvailable: true}
	catalog, err := capability.NewCatalog([]string{capability.StorageWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	releases := &atomic.Int64{}
	events := &[]AuditEvent{}
	enforcer, err := NewEnforcer(EnforcerConfig{HostInstance: "epoch", Audience: "stdio", Catalog: catalog, Clock: clock, Resolver: authorityFunc(func(context.Context, Call) (Authority, error) { return *auth, nil }), Budget: budgetFunc(func(context.Context, Authority, Call) (func(), error) { return func() { releases.Add(1) }, nil }), Audit: NewAuditor(auditFunc(func(_ context.Context, e AuditEvent) error { *events = append(*events, e); return nil }))})
	if err != nil {
		t.Fatal(err)
	}
	call := Call{Capability: capability.StorageWrite, GrantID: "grant", Operation: "host/storage/put", Target: "owner", Effect: capability.Write, Dimensions: map[string]string{"keys": "preferences"}, Usage: map[string]int64{"request_bytes": 10}, RequestID: "request", TraceID: "trace"}
	return enforcer, auth, call, cancel, releases, events
}
func failureCode(t *testing.T, err error, want capability.Code) {
	t.Helper()
	var failure *capability.Error
	if !errors.As(err, &failure) || failure.Code != want {
		t.Fatalf("expected %s; got %v", want, err)
	}
}
func TestEnforcementBeforeEffects(t *testing.T) {
	cases := []struct {
		name   string
		code   capability.Code
		change func(*Enforcer, *Authority, *Call)
	}{
		{"unauthenticated", capability.Unauthenticated, func(e *Enforcer, a *Authority, c *Call) { a.Authenticated = false }},
		{"forged actor", capability.Unauthenticated, func(e *Enforcer, a *Authority, c *Call) { a.Actor.ID = "other" }},
		{"wrong kind", capability.Unauthenticated, func(e *Enforcer, a *Authority, c *Call) { a.Actor.Kind = "unverified" }},
		{"wrong tuple", capability.TargetUnavailable, func(e *Enforcer, a *Authority, c *Call) { a.Grant.OwnerGeneration++ }},
		{"wrong audience", capability.Unauthenticated, func(e *Enforcer, a *Authority, c *Call) { a.Audience = "other" }},
		{"inactive", capability.TargetUnavailable, func(e *Enforcer, a *Authority, c *Call) { a.Active = false }},
		{"unavailable target", capability.TargetUnavailable, func(e *Enforcer, a *Authority, c *Call) { a.TargetAvailable = false }},
		{"no lease", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) { a.LeaseContext = nil }},
		{"no binding", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) { a.TransportScope = nil }},
		{"grant id", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) { c.GrantID = "other" }},
		{"grant name", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) { c.Capability = capability.StorageRead }},
		{"future grant", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.Grant.IssuedAt = e.clock().Now().Add(time.Second).Format(time.RFC3339Nano)
		}},
		{"expired", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.Grant.IssuedAt = e.clock().Now().Add(-time.Minute).Format(time.RFC3339Nano)
			a.Grant.ExpiresAt = e.clock().Now().Format(time.RFC3339Nano)
		}},
		{"policy withdrawal", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) { a.PolicyRevision = "new-review" }},
		{"version", capability.UnsupportedCapability, func(e *Enforcer, a *Authority, c *Call) { a.Grant.SchemaVersion = 2 }},
		{"scope top field", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.Grant.Scope = append(a.Grant.Scope[:len(a.Grant.Scope)-1], []byte(`,"ambient":true}`)...)
		}},
		{"scope duplicate", capability.CapabilityDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.Grant.Scope = json.RawMessage(`{"allowlists":{},"allowlists":{},"limits":{}}`)
		}},
		{"widened binding", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.TransportScope.Allowlists["keys"] = []string{"preferences", "other"}
		}},
		{"operation", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) { c.Operation = "host/storage/delete" }},
		{"target", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) { c.Target = "other" }},
		{"effect", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) { c.Effect = capability.Destructive }},
		{"key", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) { c.Dimensions["keys"] = "other" }},
		{"missing key", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) { delete(c.Dimensions, "keys") }},
		{"extra dimension", capability.InvalidRequest, func(e *Enforcer, a *Authority, c *Call) { c.Dimensions["ambient"] = "yes" }},
		{"override core", capability.InvalidRequest, func(e *Enforcer, a *Authority, c *Call) { c.Dimensions["targets"] = "owner" }},
		{"budget", capability.BudgetExceeded, func(e *Enforcer, a *Authority, c *Call) { c.Usage["request_bytes"] = 101 }},
		{"missing budget", capability.InvalidRequest, func(e *Enforcer, a *Authority, c *Call) { delete(c.Usage, "request_bytes") }},
		{"missing reservation", capability.BudgetExceeded, func(e *Enforcer, a *Authority, c *Call) { e.Budget = nil }},
		{"caller policy absent", capability.Unauthenticated, func(e *Enforcer, a *Authority, c *Call) {
			a.Background = false
			a.InitiatingCaller = &Subject{SessionClient, "caller"}
		}},
		{"caller policy denies", capability.ScopeDenied, func(e *Enforcer, a *Authority, c *Call) {
			a.Background = false
			a.InitiatingCaller = &Subject{SessionClient, "caller"}
			policy := cloneScope(a.Policy)
			policy.Allowlists["keys"] = []string{}
			a.CallerPolicy = &policy
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, a, c, _, releases, events := enforcementFixture(t)
			tc.change(e, a, &c)
			effects := 0
			err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil })
			failureCode(t, err, tc.code)
			if effects != 0 || releases.Load() != 0 {
				t.Fatal("refusal caused effects or reserved budget")
			}
			if len(*events) != 1 || (*events)[0].Outcome != tc.code || (*events)[0].RequestID != c.RequestID {
				t.Fatal("denial audit missing")
			}
			if a.Authenticated && (*events)[0].Actor != (Actor{string(a.Actor.Kind), a.Actor.ID}) {
				t.Fatal("authenticated denial actor lost")
			}
		})
	}
}
func TestDelegatedSuccessAuditAndFailingSink(t *testing.T) {
	e, a, c, _, releases, events := enforcementFixture(t)
	a.Background = false
	a.InitiatingCaller = &Subject{SessionClient, "caller"}
	policy := cloneScope(a.Policy)
	a.CallerPolicy = &policy
	effects := 0
	err := e.Run(context.Background(), c, func(_ context.Context, p *Permit) error {
		if err := p.Recheck(); err != nil {
			return err
		}
		effects++
		return nil
	})
	if err != nil || effects != 1 || releases.Load() != 1 {
		t.Fatalf("success: %v", err)
	}
	event := (*events)[0]
	if event.Actor.ID != "owner" || event.InitiatingCaller.ID != "caller" || event.Owner != a.Owner || event.Outcome != "" || event.EffectState != capability.Committed {
		t.Fatal("delegated audit lost metadata")
	}
	e.Audit = NewAuditor(auditFunc(func(context.Context, AuditEvent) error { return errors.New("sink failure") }))
	if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil }); err != nil {
		t.Fatal("sink failure denied authorized call")
	}
	c.Target = "other"
	err = e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil })
	failureCode(t, err, capability.ScopeDenied)
	if e.Counters().Allowed != 2 || e.Counters().Denied != 1 || e.Counters().AuditFailures != 2 {
		t.Fatal("enforcement counters lost with failing sink")
	}
	if effects != 2 || e.Audit.Failures() != 2 {
		t.Fatal("sink failure changed enforcement or was not reported")
	}
}
func TestPermitRechecksAndCancels(t *testing.T) {
	e, a, c, cancel, releases, _ := enforcementFixture(t)
	permit, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.Dimensions["keys"] = "other"
	if err := permit.Recheck(); err != nil {
		t.Fatal("permit aliases external demands")
	}
	a.Policy.Allowlists["keys"] = []string{}
	failureCode(t, permit.Recheck(), capability.ScopeDenied)
	cancel()
	select {
	case <-permit.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("withdrawal did not cancel permit")
	}
	permit.Close()
	permit.Close()
	if releases.Load() != 1 {
		t.Fatal("budget release not once")
	}
}
func TestExpiryAndReservationWithdrawal(t *testing.T) {
	e, _, c, _, _, _ := enforcementFixture(t)
	permit, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Close()
	e.Clock.(*fakeClock).Advance(time.Minute)
	if permit.Context.Err() == nil {
		t.Fatal("grant expiry did not cancel permit")
	}
	if err := permit.Recheck(); err == nil {
		t.Fatal("expired permit passed commit recheck")
	}
	e, _, c, cancel, releases, _ := enforcementFixture(t)
	e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
		cancel()
		return func() { releases.Add(1) }, nil
	})
	effects := 0
	err = e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil })
	if err == nil || effects != 0 || releases.Load() != 1 {
		t.Fatal("withdrawal while reserving admitted effect or leaked budget")
	}
}
func TestRateLimitAndUnknownOutcome(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
		return nil, refusal(capability.RateLimited, c.Capability)
	})
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("rate limit bypass"); return nil })
	failureCode(t, err, capability.RateLimited)
	if releases.Load() != 0 {
		t.Fatal("unreserved budget released")
	}
	e, _, c, _, releases, _ = enforcementFixture(t)
	err = e.Run(context.Background(), c, func(context.Context, *Permit) error {
		return &capability.Error{Code: capability.UnknownOutcome, EffectState: capability.Unknown}
	})
	failureCode(t, err, capability.UnknownOutcome)
	var failure *capability.Error
	errors.As(err, &failure)
	data, _ := failure.RPCData()
	if data.Retryable || data.EffectState != capability.Unknown || releases.Load() != 1 {
		t.Fatal("ambiguous effect retried or lease leaked")
	}
}
func TestLateCancellationPreservesDefiniteCommit(t *testing.T) {
	e, _, c, cancel, releases, events := enforcementFixture(t)
	effects := 0
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; cancel(); return nil })
	if err != nil || effects != 1 || releases.Load() != 1 || (*events)[0].EffectState != capability.Committed {
		t.Fatal("committed effect rewritten as cancellation")
	}
}

func TestProvisionalLoggingHasNoOtherServiceAuthority(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	a.Active = false
	a.ProvisionalLogging = true
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("provisional storage write"); return nil }), capability.TargetUnavailable)
	catalog, err := capability.NewCatalog([]string{capability.LogWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Catalog = catalog
	scope := capability.Scope{Allowlists: map[string][]string{"operations": {"host/log"}, "targets": {"owner"}, "effects": {"write"}, "levels": {"info"}}, Limits: map[string]int64{"request_bytes": 100, "rate_per_minute": 10}}
	raw, _ := json.Marshal(scope)
	a.Grant.Name = capability.LogWrite
	a.Grant.Scope = raw
	a.Policy = cloneScope(scope)
	a.TransportScope = &scope
	c.Capability = capability.LogWrite
	c.Operation = "host/log"
	c.Dimensions = map[string]string{"levels": "info"}
	c.Usage = map[string]int64{"request_bytes": 10, "rate_per_minute": 1}
	effects := 0
	if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil }); err != nil || effects != 1 {
		t.Fatal("explicit provisional logging refused")
	}
}
func TestExecutionPanicAndRawFailureAreUnknownMutations(t *testing.T) {
	for _, fn := range []func(context.Context, *Permit) error{
		func(context.Context, *Permit) error { panic("private diagnostic") },
		func(context.Context, *Permit) error { return errors.New("private diagnostic") },
	} {
		e, _, c, _, releases, events := enforcementFixture(t)
		err := e.Run(context.Background(), c, fn)
		failureCode(t, err, capability.UnknownOutcome)
		if releases.Load() != 1 || len(*events) != 1 || (*events)[0].EffectState != capability.Unknown {
			t.Fatal("panic/error leaked reservation or invented effect state")
		}
	}
}
func TestPerCallDeadlineCannotBeOmittedOrExtended(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	scope := capability.Scope{Allowlists: map[string][]string{"operations": {"host/egress/request"}, "targets": {"owner"}, "effects": {"write"}, "destinations": {"POST https://example.invalid"}}, Limits: map[string]int64{"request_bytes": 100, "response_bytes": 100, "deadline_ms": 1000}}
	raw, _ := json.Marshal(scope)
	catalog, _ := capability.NewCatalog([]string{capability.EgressRequest}, nil)
	e.Catalog = catalog
	a.Grant.Name = capability.EgressRequest
	a.Grant.Scope = raw
	a.Policy = cloneScope(scope)
	a.TransportScope = &scope
	c.Capability = capability.EgressRequest
	c.Operation = "host/egress/request"
	c.Dimensions = map[string]string{"destinations": "POST https://example.invalid"}
	c.Usage = map[string]int64{"request_bytes": 10, "response_bytes": 10, "deadline_ms": 100}
	permit, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Close()
	e.Clock.(*fakeClock).Advance(100 * time.Millisecond)
	failureCode(t, permit.Recheck(), capability.DeadlineExceeded)
	c.Usage["deadline_ms"] = 0
	_, err = e.RequireCapability(context.Background(), c)
	failureCode(t, err, capability.DeadlineExceeded)
	c.Usage["deadline_ms"] = 1001
	_, err = e.RequireCapability(context.Background(), c)
	failureCode(t, err, capability.BudgetExceeded)
}

func TestNonPluginTransportScopeCannotBeOmittedOrWidened(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	a.Actor = Subject{SessionClient, "session"}
	a.Background = false
	a.InitiatingCaller = &Subject{UserActor, "user"}
	policy := cloneScope(a.Policy)
	a.CallerPolicy = &policy
	a.TransportScope = nil
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("credential scope bypass"); return nil }), capability.CapabilityDenied)
	scope := cloneScope(a.Policy)
	scope.Allowlists["keys"] = []string{"preferences", "other"}
	a.TransportScope = &scope
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("widened credential"); return nil }), capability.ScopeDenied)
	scope = cloneScope(a.Policy)
	a.TransportScope = &scope
	if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { return nil }); err != nil {
		t.Fatal("verified scoped client refused")
	}
}
func TestControlFailuresKeepRequestAndAuditReason(t *testing.T) {
	e, _, c, _, _, events := enforcementFixture(t)
	e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
		return nil, &capability.Error{Code: capability.CapabilityDenied, EffectState: capability.NotStarted, Detail: capability.CallbackCycle}
	})
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("cycle bypass"); return nil })
	failureCode(t, err, capability.CapabilityDenied)
	var failure *capability.Error
	errors.As(err, &failure)
	if failure.RequestID != c.RequestID || (*events)[0].Reason != capability.CallbackCycle {
		t.Fatal("request/reason metadata lost")
	}
	if e.Audit.Denials()[DenialKey{capability.CapabilityDenied, capability.CallbackCycle}] != 1 {
		t.Fatal("cycle denial counter absent")
	}
}

func TestEachAuthorityScopeActuallyNarrowsCall(t *testing.T) {
	for _, plane := range []string{"grant", "policy", "transport", "caller"} {
		t.Run(plane, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			narrowed := cloneScope(a.Policy)
			narrowed.Allowlists["keys"] = []string{}
			switch plane {
			case "grant":
				a.Grant.Scope, _ = json.Marshal(narrowed)
				a.TransportScope = &narrowed
			case "policy":
				a.Policy = narrowed
			case "transport":
				a.TransportScope = &narrowed
			case "caller":
				a.Background = false
				a.InitiatingCaller = &Subject{SessionClient, "caller"}
				a.CallerPolicy = &narrowed
			}
			failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("authority plane ignored"); return nil }), capability.ScopeDenied)
		})
	}
}

func TestPermitPinsAuthenticatedPrincipal(t *testing.T) {
	for _, field := range []string{"actor", "owner", "audience", "caller", "revision"} {
		t.Run(field, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			a.Actor = Subject{SessionClient, "session"}
			a.Background = false
			a.InitiatingCaller = &Subject{AgentClient, "initiator"}
			policy := cloneScope(a.Policy)
			a.CallerPolicy = &policy
			permit, err := e.RequireCapability(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer permit.Close()
			switch field {
			case "actor":
				a.Actor.ID = "another"
			case "owner":
				a.Owner.OwnerGeneration++
				a.Grant.OwnerGeneration++
			case "audience":
				a.Audience = "new-audience"
				a.Grant.Audience = a.Audience
			case "caller":
				a.InitiatingCaller.ID = "another"
			case "revision":
				a.PolicyRevision = "next"
				a.Grant.PolicyRevision = "next"
			}
			want := capability.Unauthenticated
			if field == "owner" {
				want = capability.TargetUnavailable
			}
			failureCode(t, permit.Recheck(), want)
		})
	}
}

func TestRequestCancellationAndUnavailableAdaptersFailBeforeEffects(t *testing.T) {
	for _, problem := range []string{"cancelled", "nil resolver", "nil catalog", "empty request", "nil release", "cancelled lease", "bad actor"} {
		t.Run(problem, func(t *testing.T) {
			e, a, c, cancel, _, _ := enforcementFixture(t)
			ctx := context.Background()
			want := capability.InternalError
			switch problem {
			case "cancelled":
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				stop()
				want = capability.Cancelled
			case "nil resolver":
				e.Resolver = nil
			case "nil catalog":
				e.Catalog = nil
			case "empty request":
				c.RequestID = ""
				want = capability.InvalidRequest
			case "nil release":
				e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) { return nil, nil })
				want = capability.BudgetExceeded
			case "cancelled lease":
				cancel()
				want = capability.CapabilityDenied
			case "bad actor":
				a.Actor.ID = "mid\ncontrol"
				a.Owner.OwnerID = a.Actor.ID
				a.Grant.OwnerID = a.Actor.ID
				want = capability.Unauthenticated
			}
			failureCode(t, e.Run(ctx, c, func(context.Context, *Permit) error { t.Fatal("effect despite unavailable authority"); return nil }), want)
		})
	}
}

func TestScopeAndTupleValidationCoverAllTrustedInputs(t *testing.T) {
	for _, problem := range []string{"host", "owner", "generation", "policy schema", "transport schema", "caller schema", "extra usage", "negative usage"} {
		t.Run(problem, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			want := capability.Unauthenticated
			switch problem {
			case "host":
				a.Grant.HostInstance = "another"
				want = capability.TargetUnavailable
			case "owner":
				a.Grant.OwnerID = "another"
				want = capability.CapabilityDenied
			case "generation":
				a.Owner.OwnerGeneration = 0
			case "policy schema":
				a.Policy.Allowlists["unrecognized"] = []string{"one"}
				want = capability.ScopeDenied
			case "transport schema":
				a.TransportScope.Allowlists["unrecognized"] = []string{"one"}
				want = capability.ScopeDenied
			case "caller schema":
				policy := cloneScope(a.Policy)
				policy.Allowlists["unrecognized"] = []string{"one"}
				a.CallerPolicy = &policy
				a.Background = false
				a.InitiatingCaller = &Subject{AgentClient, "caller"}
				want = capability.ScopeDenied
			case "extra usage":
				c.Usage["unrecognized"] = 1
				want = capability.InvalidRequest
			case "negative usage":
				c.Usage["request_bytes"] = -1
				want = capability.BudgetExceeded
			}
			failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("unchecked trusted input"); return nil }), want)
		})
	}
}

func TestRequestOwnedPermitCancellationAndWallExpiry(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	permit, err := e.RequireCapability(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Close()
	cancel()
	if permit.Context.Err() == nil {
		t.Fatal("request cancellation lost")
	}
	failureCode(t, permit.Recheck(), capability.Cancelled)
	permit.Close()
	if releases.Load() != 1 {
		t.Fatal("cancel release lost")
	}
	e, _, c, _, _, _ = enforcementFixture(t)
	permit, err = e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Close()
	clock := e.Clock.(*fakeClock)
	clock.mu.Lock()
	clock.now = clock.now.Add(time.Minute)
	clock.mu.Unlock()
	failureCode(t, permit.Recheck(), capability.CapabilityDenied)
}

func TestBudgetRefusalReleasesPartialReservation(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
		return func() { releases.Add(1) }, refusal(capability.RateLimited, c.Capability)
	})
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("budget refusal ignored"); return nil }), capability.RateLimited)
	if releases.Load() != 1 {
		t.Fatal("partial reservation leaked")
	}
}

func TestSeedCapabilitiesUseSameCallBoundary(t *testing.T) {
	cases := []struct {
		name, operation string
		effect          capability.Effect
		dimensions      map[string]string
		usage           map[string]int64
	}{
		{capability.ReadonlyQuery, "host/readonly/query", capability.Read, map[string]string{"resources": "summary", "sessions": "session", "visibility": "metadata"}, map[string]int64{"rows": 1, "request_bytes": 10, "response_bytes": 10, "deadline_ms": 100}},
		{capability.ContextSource, "retrieve", capability.Read, map[string]string{"agents": "agent", "sessions": "session", "sources": "source", "mounts": "mount"}, map[string]int64{"response_bytes": 10}},
		{capability.DurableAgentWake, "wake", capability.Write, map[string]string{"agents": "agent"}, map[string]int64{"request_bytes": 10, "rate_per_minute": 1}},
		{capability.ReflexSeed, "install", capability.Write, map[string]string{"agents": "agent", "seeds": "seed"}, map[string]int64{"request_bytes": 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, a, c, _, releases, _ := enforcementFixture(t)
			catalog, err := capability.NewCatalog([]string{tc.name}, nil)
			if err != nil {
				t.Fatal(err)
			}
			e.Catalog = catalog
			scope := capability.Scope{Allowlists: map[string][]string{"operations": {tc.operation}, "targets": {"owner"}, "effects": {string(tc.effect)}}, Limits: map[string]int64{}}
			for k, v := range tc.dimensions {
				scope.Allowlists[k] = []string{v}
			}
			for k, v := range tc.usage {
				scope.Limits[k] = v
			}
			a.Grant.Name = tc.name
			a.Grant.Scope, _ = json.Marshal(scope)
			a.Policy = cloneScope(scope)
			a.TransportScope = &scope
			c.Capability = tc.name
			c.Operation = tc.operation
			c.Effect = tc.effect
			c.Dimensions = tc.dimensions
			c.Usage = tc.usage
			effects := 0
			if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil }); err != nil {
				t.Fatal(err)
			}
			c.Target = "unapproved"
			failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil }), capability.ScopeDenied)
			if effects != 1 || releases.Load() != 1 {
				t.Fatal("seed target bypass or reservation leak")
			}
		})
	}
}

func TestWorkflowTransportBindingPinsEachContextDimension(t *testing.T) {
	descriptor := capability.Descriptor{Name: "host.example.workflow.callback", SchemaVersion: 1, Description: "Reviewed run callback", EffectCeiling: capability.Write, Operations: []string{"workflow/callback"}, ScopeSchema: capability.ScopeSchema{Allowlists: []string{"operations", "targets", "effects", "provider", "run", "step", "attempt", "fork"}, Limits: []string{"deadline_ms", "request_bytes"}}}
	for _, dimension := range []string{"provider", "run", "step", "attempt", "fork"} {
		t.Run(dimension, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			catalog, err := capability.NewCatalog(nil, []capability.Descriptor{descriptor})
			if err != nil {
				t.Fatal(err)
			}
			e.Catalog = catalog
			scope := capability.Scope{Allowlists: map[string][]string{"operations": {"workflow/callback"}, "targets": {"owner"}, "effects": {"write"}}, Limits: map[string]int64{"deadline_ms": 1000, "request_bytes": 100}}
			c.Dimensions = map[string]string{}
			for _, key := range []string{"provider", "run", "step", "attempt", "fork"} {
				scope.Allowlists[key] = []string{"one", "two"}
				c.Dimensions[key] = "one"
			}
			a.Grant.Name = descriptor.Name
			a.Grant.Scope, _ = json.Marshal(scope)
			a.Policy = cloneScope(scope)
			binding := cloneScope(scope)
			for _, key := range []string{"provider", "run", "step", "attempt", "fork"} {
				binding.Allowlists[key] = []string{"one"}
			}
			binding.Limits["deadline_ms"] = 100
			a.TransportScope = &binding
			c.Capability = descriptor.Name
			c.Operation = "workflow/callback"
			c.Usage = map[string]int64{"deadline_ms": 100, "request_bytes": 10}
			if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { return nil }); err != nil {
				t.Fatal(err)
			}
			c.Dimensions[dimension] = "two"
			failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("borrowed another callback context"); return nil }), capability.ScopeDenied)
		})
	}
}
