package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
)

func TestExplicitBackgroundOrVerifiedUserCaller(t *testing.T) {
	for _, mode := range []string{"absent", "policy only", "caller and background", "invalid caller", "user", "background"} {
		t.Run(mode, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			a.Background = false
			policy := cloneScope(a.Policy)
			switch mode {
			case "background":
				a.Background = true
			case "policy only":
				a.CallerPolicy = &policy
			case "caller and background":
				a.Background = true
				a.InitiatingCaller = &Subject{UserActor, "user"}
				a.CallerPolicy = &policy
			case "invalid caller":
				a.InitiatingCaller = &Subject{UserActor, "bad\nuser"}
				a.CallerPolicy = &policy
			case "user":
				a.InitiatingCaller = &Subject{UserActor, "user"}
				a.CallerPolicy = &policy
			}
			effects := 0
			err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil })
			if mode == "user" || mode == "background" {
				if err != nil || effects != 1 {
					t.Fatal("verified UI caller refused")
				}
			} else if err == nil || effects != 0 {
				t.Fatal("ambiguous or unverified caller admitted")
			}
		})
	}
	e, a, c, _, _, _ := enforcementFixture(t)
	policy := cloneScope(a.Policy)
	policy.Allowlists["keys"] = nil
	a.CallerPolicy = &policy
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("caller policy ignored"); return nil }), capability.InvalidRequest)
}

func TestDelegatedPluginPolicyStillIntersects(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	caller := cloneScope(a.Policy)
	a.Background = false
	a.InitiatingCaller = &Subject{UserActor, "user"}
	a.CallerPolicy = &caller
	a.Policy.Allowlists["keys"] = nil
	failureCode(t, e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("plugin policy ignored"); return nil }), capability.ScopeDenied)
}

func TestResolverPanicsFailClosedAndRelease(t *testing.T) {
	for _, api := range []string{"run", "require"} {
		for _, when := range []int64{1, 2} {
			t.Run(api+string(rune('0'+when)), func(t *testing.T) {
				e, a, c, _, releases, events := enforcementFixture(t)
				var calls atomic.Int64
				e.Resolver = authorityFunc(func(context.Context, Call) (Authority, error) {
					if calls.Add(1) == when {
						panic("private diagnostic")
					}
					return *a, nil
				})
				var err error
				if api == "run" {
					err = e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("effect"); return nil })
				} else {
					var p *Permit
					p, err = e.RequireCapability(context.Background(), c)
					if p != nil {
						p.Close()
						t.Fatal("panic returned permit")
					}
				}
				failureCode(t, err, capability.InternalError)
				if releases.Load() != when-1 || len(*events) != 1 || e.Counters().Denied != 1 {
					t.Fatal("panic leaked reservation or audit")
				}
			})
		}
	}
}

func TestReservePanicCleansLifetimeAndTimer(t *testing.T) {
	e, _, c, _, _, events := enforcementFixture(t)
	var budgetContext context.Context
	e.Budget = budgetFunc(func(ctx context.Context, _ Authority, _ Call) (func(), error) { budgetContext = ctx; panic("reserve") })
	_, err := e.RequireCapability(context.Background(), c)
	failureCode(t, err, capability.InternalError)
	if budgetContext == nil || budgetContext.Err() == nil || len(*events) != 1 {
		t.Fatal("reserve panic leaked cancellation or audit")
	}
	clock := e.Clock.(*fakeClock)
	clock.mu.Lock()
	timer := clock.timers[0]
	clock.mu.Unlock()
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	if !stopped {
		t.Fatal("reserve panic retained timer")
	}
}

func TestReleasePanicIsSeparateFromCommittedOutcome(t *testing.T) {
	e, _, c, _, _, events := enforcementFixture(t)
	var releases atomic.Int64
	e.Budget = budgetFunc(func(context.Context, Authority, Call) (func(), error) {
		return func() { releases.Add(1); panic("release") }, nil
	})
	effects := 0
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error { effects++; return nil })
	if err != nil || effects != 1 || releases.Load() != 1 || e.Counters().CleanupFailures != 1 || (*events)[0].EffectState != capability.Committed || (*events)[0].Outcome != "" {
		t.Fatal("cleanup panic rewrote definite outcome")
	}
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close()
	if p.Context.Err() == nil || releases.Load() != 2 || e.Counters().CleanupFailures != 2 {
		t.Fatal("cleanup panic escaped or repeated")
	}
}

func TestAuditSurvivesRequestCancellationAndPanic(t *testing.T) {
	e, _, c, _, _, _ := enforcementFixture(t)
	var recorded atomic.Int64
	e.Audit = NewAuditor(auditFunc(func(ctx context.Context, _ AuditEvent) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		recorded.Add(1)
		return nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.Run(ctx, c, func(context.Context, *Permit) error { cancel(); return nil }); err != nil {
		t.Fatal(err)
	}
	failureCode(t, e.Run(ctx, c, func(context.Context, *Permit) error { t.Fatal("cancelled effect"); return nil }), capability.Cancelled)
	if recorded.Load() != 2 || e.Audit.Failures() != 0 {
		t.Fatal("request cancellation dropped audit")
	}
	e.Audit = NewAuditor(auditFunc(func(context.Context, AuditEvent) error { panic("sink") }))
	if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { return nil }); err != nil || e.Audit.Failures() != 1 {
		t.Fatal("audit panic changed policy")
	}
}

func TestAuditedIdentifiersAreBoundedAndUnauthenticatedActorScrubbed(t *testing.T) {
	e, a, c, _, _, events := enforcementFixture(t)
	bad := strings.Repeat("a", MaxAuditIdentifierBytes+1) + "\n"
	c.Target = bad
	c.Server = bad
	c.Tool = bad
	c.TraceID = bad
	c.RequestID = bad
	c.GrantID = bad
	c.Capability = bad
	a.Actor.ID = bad
	a.InitiatingCaller = &Subject{UserActor, bad}
	a.Owner.HostInstance = bad
	a.Owner.OwnerID = bad
	a.PolicyRevision = bad
	a.Authenticated = false
	if err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("effect"); return nil }); err == nil {
		t.Fatal("unverified actor")
	}
	event := (*events)[0]
	if event.Actor != (Actor{}) || event.InitiatingCaller != nil {
		t.Fatal("unverified identity leaked")
	}
	for _, id := range []string{event.Target, event.Server, event.Tool, event.TraceID, event.RequestID, event.GrantID, event.Capability, event.Owner.HostInstance, event.Owner.OwnerID, event.PolicyRevision} {
		if id != invalidAuditIdentifier {
			t.Fatal("unbounded audit identifier")
		}
	}
}

func TestEnforcerExpectedEpochAndAudienceAreIndependent(t *testing.T) {
	for _, field := range []string{"epoch", "audience"} {
		t.Run(field, func(t *testing.T) {
			e, a, c, _, _, _ := enforcementFixture(t)
			want := capability.Unauthenticated
			if field == "epoch" {
				a.Owner.HostInstance = "foreign"
				a.Grant.HostInstance = "foreign"
				want = capability.TargetUnavailable
			} else {
				a.Audience = "foreign"
				a.Grant.Audience = "foreign"
			}
			err := e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("resolver compared its own forged values"); return nil })
			failureCode(t, err, want)
			if field == "epoch" {
				var f *capability.Error
				errors.As(err, &f)
				if f.Detail != capability.StaleBinding {
					t.Fatal("epoch failure lost detail")
				}
			}
		})
	}
	if _, err := NewEnforcer(EnforcerConfig{}); err == nil {
		t.Fatal("unfixed enforcer constructed")
	}
}

func TestStaleGenerationDetailOnAdmissionAndRecheck(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	a.Owner.OwnerGeneration++
	a.Grant.OwnerGeneration++
	for _, err := range []error{p.Recheck(), func() error {
		a.Grant.OwnerGeneration--
		return e.Run(context.Background(), c, func(context.Context, *Permit) error { t.Fatal("stale effect"); return nil })
	}()} {
		failureCode(t, err, capability.TargetUnavailable)
		var f *capability.Error
		errors.As(err, &f)
		if f.Detail != capability.StaleBinding {
			t.Fatal("stale detail missing")
		}
	}
}

func TestPermitCommitIsSingleUseAndRetainsRunningBudget(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	result := make(chan error, 1)
	var effects atomic.Int64
	go func() {
		result <- p.Commit(func(context.Context) error { close(started); <-finish; effects.Add(1); return nil })
	}()
	<-started
	p.Close()
	if releases.Load() != 0 || p.Context.Err() == nil {
		t.Fatal("in-flight permit released or not cancelled")
	}
	failureCode(t, p.Commit(func(context.Context) error { effects.Add(1); return nil }), capability.Conflict)
	close(finish)
	if err := <-result; err != nil || releases.Load() != 1 || effects.Load() != 1 {
		t.Fatal("definite commit or release lost")
	}
}

func TestPermitZeroValuesAndCloseCancellation(t *testing.T) {
	var nilPermit *Permit
	for _, p := range []*Permit{nilPermit, {}} {
		failureCode(t, p.Recheck(), capability.InvalidRequest)
		p.Close()
		failureCode(t, p.Commit(func(context.Context) error { return nil }), capability.InvalidRequest)
	}
	e, _, c, _, _, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if p.Context.Err() == nil {
		t.Fatal("Close did not cancel")
	}
}

func TestCancellationCauseCannotClaimCommittedEffect(t *testing.T) {
	e, _, c, _, _, _ := enforcementFixture(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(&capability.Error{Code: capability.Conflict, EffectState: capability.Committed})
	err := e.Run(ctx, c, func(context.Context, *Permit) error { t.Fatal("effect"); return nil })
	failureCode(t, err, capability.Cancelled)
	var f *capability.Error
	errors.As(err, &f)
	if f.EffectState != capability.NotStarted {
		t.Fatal("caller invented effect state")
	}
}

func TestWriteTypedUnknownStateBecomesUnknownOutcome(t *testing.T) {
	e, _, c, _, _, _ := enforcementFixture(t)
	err := e.Run(context.Background(), c, func(context.Context, *Permit) error {
		return &capability.Error{Code: capability.Cancelled, EffectState: capability.Unknown}
	})
	failureCode(t, err, capability.UnknownOutcome)
}

func TestScopeDecoderRejectsCaseVariants(t *testing.T) {
	for _, raw := range []string{
		`{"allowlists":{"keys":["one"]},"Allowlists":{"keys":["two"]},"limits":{}}`,
		`{"Allowlists":{},"limits":{}}`,
		`{"allowlists":{},"Limits":{}}`,
		`{"allowlists":{},"limits":{},"limits":{}}`,
	} {
		if _, err := decodeScope("scope", json.RawMessage(raw)); err == nil {
			t.Fatal("ambiguous scope accepted")
		}
	}
}

func TestDeadlineRecheckDoesNotDependOnTimer(t *testing.T) {
	e, a, c, _, _, _ := enforcementFixture(t)
	scope := capability.Scope{Allowlists: map[string][]string{"operations": {"host/egress/request"}, "targets": {"owner"}, "effects": {"write"}, "destinations": {"POST https://example.invalid"}}, Limits: map[string]int64{"request_bytes": 100, "response_bytes": 100, "deadline_ms": 1000}}
	catalog, err := capability.NewCatalog([]string{capability.EgressRequest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Catalog = catalog
	a.Grant.Name = capability.EgressRequest
	a.Grant.Scope, _ = json.Marshal(scope)
	a.Policy = cloneScope(scope)
	a.TransportScope = &scope
	c.Capability = capability.EgressRequest
	c.Operation = "host/egress/request"
	c.Dimensions = map[string]string{"destinations": "POST https://example.invalid"}
	c.Usage = map[string]int64{"request_bytes": 10, "response_bytes": 10, "deadline_ms": 100}
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Advance the wall clock without firing the eager timer.
	clock := e.Clock.(*fakeClock)
	clock.mu.Lock()
	clock.now = clock.now.Add(100 * time.Millisecond)
	clock.mu.Unlock()
	failureCode(t, p.Recheck(), capability.DeadlineExceeded)
}
func TestContextCheckedBeforeResolver(t *testing.T) {
	e, _, c, _, _, _ := enforcementFixture(t)
	e.Resolver = authorityFunc(func(context.Context, Call) (Authority, error) {
		t.Fatal("resolver invoked for cancelled request")
		return Authority{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failureCode(t, e.Run(ctx, c, func(context.Context, *Permit) error { t.Fatal("effect"); return nil }), capability.Cancelled)
}

func TestAuditIdentifierBoundaries(t *testing.T) {
	valid := strings.Repeat("a", MaxAuditIdentifierBytes)
	if auditIdentifier(valid) != valid {
		t.Fatal("valid maximum identifier lost")
	}
	for _, value := range []string{valid + "a", "mid\ncontrol", " padded", string([]byte{0xff})} {
		if auditIdentifier(value) != invalidAuditIdentifier {
			t.Fatal("unsafe identifier retained")
		}
	}
}

type panicStopTimer struct{}

func (panicStopTimer) Stop() bool { panic("timer stop") }
func TestCleanupContinuesAfterTimerPanic(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	p.timer = panicStopTimer{}
	p.Close()
	p.Close()
	if releases.Load() != 1 || p.Context.Err() == nil || e.Counters().CleanupFailures != 1 {
		t.Fatal("one cleanup panic skipped remaining cleanup")
	}
}

func TestCleanupRunsAfterCallbackErrorAndInvalidTypedFailure(t *testing.T) {
	for _, failure := range []error{errors.New("private"), &capability.Error{Code: "arbitrary", EffectState: capability.Committed}} {
		e, _, c, _, releases, events := enforcementFixture(t)
		err := e.Run(context.Background(), c, func(context.Context, *Permit) error { return failure })
		failureCode(t, err, capability.UnknownOutcome)
		if releases.Load() != 1 || (*events)[0].EffectState != capability.Unknown {
			t.Fatal("callback error skipped cleanup or invented result")
		}
	}
}

func TestRecheckGenerationReplacementAfterOldLeaseCancellation(t *testing.T) {
	e, a, c, cancel, _, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cancel()
	a.Owner.OwnerGeneration++
	a.Grant.OwnerGeneration++
	life, stop := context.WithCancel(context.Background())
	defer stop()
	a.LeaseContext = life
	err = p.Recheck()
	failureCode(t, err, capability.TargetUnavailable)
	var f *capability.Error
	errors.As(err, &f)
	if f.Detail != capability.StaleBinding {
		t.Fatal("old cancellation hid generation fencing")
	}
}
func TestConcurrentCommitHasOneWinner(t *testing.T) {
	e, _, c, _, releases, _ := enforcementFixture(t)
	p, err := e.RequireCapability(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	var effects atomic.Int64
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() { <-start; results <- p.Commit(func(context.Context) error { effects.Add(1); return nil }) }()
	}
	close(start)
	winners := 0
	for range 8 {
		if err := <-results; err == nil {
			winners++
		} else {
			failureCode(t, err, capability.Conflict)
		}
	}
	if winners != 1 || effects.Load() != 1 || releases.Load() != 1 {
		t.Fatal("commit permit reused concurrently")
	}
}
