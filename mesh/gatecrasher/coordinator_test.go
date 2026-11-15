package gatecrasher

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/gate/gatetest"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/security/policy"
)

func TestAllowOnceAndDenyHaveSeparateDeliveryAndEffect(t *testing.T) {
	for _, option := range []string{"native-allow-once", "native-deny"} {
		t.Run(option, func(t *testing.T) {
			f := newFixture()
			ticket := f.ticket(t)
			res := f.respond(t, ticket, option)
			if res.State != gate.Decided || res.Decision.OptionID != option {
				t.Fatal(res)
			}
			p, err := f.c.Deliver(human(), ticket)
			if err != nil || p.Status != gate.DeliveryAcknowledged {
				t.Fatal(p, err)
			}
			route, _ := f.journal.Load(human(), f.request.IdempotencyKey)
			if len(route.Executions) != 0 {
				t.Fatal("delivery manufactured execution")
			}
			executor := &fakeExecutor{}
			err = executor.execute(human(), f.request, *res.Decision, admission(f), f.auth)
			if option == "native-deny" {
				if !errors.Is(err, gate.ErrDenied) || executor.effects != 0 {
					t.Fatal(err, executor.effects)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = executor.execute(human(), f.request, *res.Decision, admission(f), f.auth); !errors.Is(err, gate.ErrDenied) || executor.effects != 1 {
				t.Fatal(err, executor.effects)
			}
			if err = f.c.RecordExecution(human(), gate.ExecutionReceipt{DecisionRef: p.DecisionRef, OperationID: f.request.OperationID, Binding: f.request.Binding, State: gate.ExecutionSucceeded, EffectRef: "fake-effect-1"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompetingRespondersChooseOneImmutableOutcome(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	for _, option := range []string{"native-allow-once", "native-deny"} {
		wg.Add(1)
		go func(option string) {
			defer wg.Done()
			<-start
			if _, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: option, ExpectedRevision: ticket.Handle.Revision}); err == nil {
				winners.Add(1)
			}
		}(option)
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("one authoritative response must win", winners.Load())
	}
	first, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Cancel(human(), ticket, hitl.CauseCallerWithdrawn); err == nil {
		t.Fatal("cancel replaced winner")
	}
	after, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || !reflect.DeepEqual(first, after) {
		t.Fatal(after, err)
	}
}

func TestRequestReplayReconnectSnapshotIsolationAndConflicts(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.restart()
	same, err := f.c.Request(human(), f.request)
	if err != nil || !reflect.DeepEqual(ticket, same) {
		t.Fatal(same, err)
	}
	changes := map[string]func(*gate.Request){
		"arguments":               func(r *gate.Request) { r.OperationDigest = gatetest.Digest("tool-v1", "edited args") },
		"plan-version":            func(r *gate.Request) { r.ResourceVersions[0].Revision = "2" },
		"generation":              func(r *gate.Request) { r.Binding.Generation++ },
		"option-set":              func(r *gate.Request) { r.Options[0].Payload = []byte(`{"changed":true}`) },
		"scope":                   func(r *gate.Request) { r.Options[0].Scope.Kind = gate.Session },
		"string-number-native-id": func(r *gate.Request) { r.NativeRequestID = gate.NativeRequestID(`"9007199254740993"`) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r, _ := f.request.Snapshot()
			change(&r)
			if _, err := f.c.Request(human(), r); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
	f.request.Options[0].Payload[2] = 'X'
	f.request.NativeRequestID[0] = '1'
	stored, _ := f.journal.Load(human(), ticketBindingKey())
	if string(stored.Request.Options[0].Payload) != `{"native":"once"}` || !stored.Request.NativeRequestID.Equal(gate.NativeRequestID(`9007199254740993`)) {
		t.Fatal("caller mutation altered accepted snapshot")
	}
}
func ticketBindingKey() string { return "fake-request-1" }

func TestCapabilitiesRefuseUnsupportedNativeSemanticsAndModes(t *testing.T) {
	for _, change := range []func(*fixture){
		func(f *fixture) { f.request.Options[0].Scope.Kind = gate.Session },
		func(f *fixture) { f.request.Options[0].Kind = "unknown" },
		func(f *fixture) { f.request.Obligations[0].Kind = "dry_run_only" },
		func(f *fixture) { f.request.Obligations[0].Kind = "rate_limit" },
		func(f *fixture) { f.request.Adapter.LaunchMode = "bypass" },
		func(f *fixture) { f.request.OperationClass = "grouped-network" },
		func(f *fixture) { f.origin.cap.ScopeKinds = nil },
		func(f *fixture) { f.origin.cap.ObligationKinds = nil },
	} {
		f := newFixture()
		change(f)
		if _, err := f.c.Request(human(), f.request); err == nil {
			t.Fatal("unsupported request accepted")
		}
		if _, err := f.journal.Load(human(), f.request.IdempotencyKey); !errors.Is(err, ErrNotFound) {
			t.Fatal("unsupported request persisted", err)
		}
	}
}

func TestWaitCancellationNeverWithdrawsAndExplicitCancelIsDistinct(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	ctx, cancel := context.WithCancel(human())
	cancel()
	if _, err := f.c.Await(ctx, ticket, gate.WaitOptions{MaxWait: time.Second}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	pending, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || pending.State != gate.Pending {
		t.Fatal(pending, err)
	}
	if _, err = f.c.Cancel(human(), ticket, hitl.CauseParticipantCanceled); !errors.Is(err, gate.ErrUnsupported) {
		t.Fatal(err)
	}
	cancelled, err := f.c.Cancel(human(), ticket, hitl.CauseCallerWithdrawn)
	if err != nil || cancelled.State != gate.Cancelled || cancelled.Decision != nil {
		t.Fatal(cancelled, err)
	}
}

func TestExpiryResponseAndCancellationRacesWithoutSweeper(t *testing.T) {
	t.Run("at-deadline", func(t *testing.T) {
		f := newFixture()
		ticket := f.ticket(t)
		f.clock.advance(time.Minute)
		_, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: "native-allow-once", ExpectedRevision: ticket.Handle.Revision})
		if err == nil {
			t.Fatal("late response admitted")
		}
		res, err := f.c.Await(human(), ticket, gate.WaitOptions{})
		if err != nil || res.State != gate.Expired {
			t.Fatal(res, err)
		}
		if _, err := f.c.Deliver(human(), ticket); err == nil || f.origin.sends != 0 {
			t.Fatal("expired native request delivered", err)
		}
	})
	t.Run("cancel-response", func(t *testing.T) {
		f := newFixture()
		ticket := f.ticket(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var winners atomic.Int32
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := f.c.Respond(human(), ticket, ResponseAttempt{OptionID: "native-allow-once", ExpectedRevision: ticket.Handle.Revision}); err == nil {
				winners.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			if _, err := f.c.Cancel(human(), ticket, hitl.CauseCallerWithdrawn); err == nil {
				winners.Add(1)
			}
		}()
		close(start)
		wg.Wait()
		if winners.Load() != 1 {
			t.Fatal(winners.Load())
		}
		res, err := f.c.Await(human(), ticket, gate.WaitOptions{})
		if err != nil || res.State != gate.Decided && res.State != gate.Cancelled {
			t.Fatal(res, err)
		}
	})
}

func TestInvalidAuthoritySelfApprovalAndIssuerSpoofCannotDecide(t *testing.T) {
	for _, mode := range []string{"unauthenticated", "self-approval", "proof-spoof", "issuer-spoof"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			if mode == "issuer-spoof" {
				f.request.Policy.IssuerRef = "caller-supplied"
				f.origin.live = f.request
				if _, err := f.c.Request(human(), f.request); !errors.Is(err, ErrPolicy) {
					t.Fatal(err)
				}
				return
			}
			ticket := f.ticket(t)
			ctx := human()
			a := ResponseAttempt{OptionID: "native-allow-once", ExpectedRevision: ticket.Handle.Revision}
			if mode == "self-approval" {
				a.Participant.PrincipalRef = string(f.request.Binding.OperativeID)
			}
			if mode == "unauthenticated" {
				ctx = context.Background()
			}
			if mode == "proof-spoof" {
				ctx = context.Background()
				a.Participant = hitl.Participant{PrincipalRef: "urn:human:alice", Authority: "verified", Assurance: "passkey", Proof: &hitl.Proof{Scheme: "webauthn", KeyRef: "fake", Binds: []hitl.ProofBind{}}}
			}
			if _, err := f.c.Respond(ctx, ticket, a); !errors.Is(err, gate.ErrAuthority) {
				t.Fatal(err)
			}
			pending, err := f.c.Await(human(), ticket, gate.WaitOptions{})
			if err != nil || pending.State != gate.Pending {
				t.Fatal(pending, err)
			}
			if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, ErrPending) || f.origin.sends != 0 {
				t.Fatal(err)
			}
		})
	}
}

func TestPolicyIntentAuditFailureModesAndNoApprovalFallback(t *testing.T) {
	for _, mode := range []string{"deny", "allow", "audit-failed", "pdp-closed", "pdp-open", "shadow", "dry-run", "rate"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			want := ErrPolicy
			switch mode {
			case "deny":
				f.pdp.decision.Effect = policy.Deny
			case "allow":
				f.pdp.decision.Effect = policy.Allow
				f.pdp.decision.Obligations = nil
				f.request.Obligations = []gate.ObligationRef{}
				f.origin.live = f.request
				want = ErrLocalPath
			case "audit-failed":
				f.audit.fail = true
			case "pdp-closed":
				f.pdp.err = errCrash
			case "pdp-open":
				f.pdp.err = errCrash
				f.c.cfg.Policy.Options.FailMode = policy.FailOpenWithAudit
				want = ErrLocalPath
			case "shadow":
				f.c.cfg.Policy.Options.Mode = policy.Shadow
				want = ErrLocalPath
			case "dry-run":
				f.pdp.decision.Obligations = append(f.pdp.decision.Obligations, policy.Obligation{Kind: policy.DryRunOnly, PolicyID: "rule-1", Reference: "preview"})
			case "rate":
				f.pdp.decision.Obligations = append(f.pdp.decision.Obligations, policy.Obligation{Kind: policy.RateLimit, PolicyID: "rule-1", Reference: "rate", Limit: &policy.Limit{Count: 1, Window: time.Hour, Unit: "calls"}})
			}
			_, err := f.c.Request(human(), f.request)
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			if f.origin.sends != 0 {
				t.Fatal("policy refusal delivered approval")
			}
		})
	}
	f := newFixture()
	ticket := f.ticket(t)
	f.pdp.err = errCrash
	f.audit.fail = true
	pending, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || pending.State != gate.Pending {
		t.Fatal("PDP outage changed pending human state", pending, err)
	}
}

func TestOwnerRevisionAndNativeReceiptMismatch(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	for _, change := range []func(*gate.Ticket){func(t *gate.Ticket) { t.AuthorityRef = "other-owner" }, func(t *gate.Ticket) { t.Handle.Revision++ }, func(t *gate.Ticket) { t.Binding.Generation++ }, func(t *gate.Ticket) { t.Handle.ItemID = "other-item" }} {
		forged := ticket
		change(&forged)
		if _, err := f.c.Await(human(), forged, gate.WaitOptions{}); err == nil {
			t.Fatal("forged ticket accepted")
		}
	}
	res := f.respond(t, ticket, "native-allow-once")
	p, err := f.c.Deliver(human(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	p.NativeRequestID = gate.NativeRequestID(`"9007199254740993"`)
	if err = f.c.RecordDelivery(human(), p); !errors.Is(err, gate.ErrBinding) {
		t.Fatal(err)
	}
	forged := *res.Decision
	forged.AuthorityRef = "spoof"
	if err = (&fakeExecutor{}).execute(human(), f.request, forged, admission(f), f.auth); !errors.Is(err, gate.ErrAuthority) {
		t.Fatal(err)
	}
}

func TestChangedLiveOperationExpiryOrGenerationNeverSendsAllowOrEffects(t *testing.T) {
	for _, change := range []func(*fixture){
		func(f *fixture) { f.origin.live.OperationDigest = gatetest.Digest("tool-v1", "edited arguments") },
		func(f *fixture) {
			f.origin.live.ResourceVersions = slices.Clone(f.origin.live.ResourceVersions)
			f.origin.live.ResourceVersions[0].Revision = "new-plan"
			f.origin.live.OperationDigest = gatetest.Digest("tool-v1", "edited plan")
		},
		func(f *fixture) { f.origin.live.Binding.Generation = 8 },
		func(f *fixture) { f.clock.advance(time.Minute) },
		func(f *fixture) { f.auth.deny = true },
	} {
		f := newFixture()
		ticket := f.ticket(t)
		res := f.respond(t, ticket, "native-allow-once")
		change(f)
		if _, err := f.c.Deliver(human(), ticket); err == nil || f.origin.sends != 0 {
			t.Fatal("invalid allow emitted", err)
		}
		e := &fakeExecutor{}
		live := admission(f)
		live.OperationDigest = f.origin.live.OperationDigest
		live.Binding = f.origin.live.Binding
		if err := e.execute(human(), f.request, *res.Decision, live, f.auth); err == nil {
			t.Fatal("invalid operation admitted")
		}
	}
}

func TestGrantExpiryBeforeRequestDeadlineRefusesDeliveryAndEffect(t *testing.T) {
	f := newFixture()
	for i := range f.request.Options {
		f.request.Options[i].Scope.ExpiresAt = f.clock.get().Add(10 * time.Second)
	}
	f.origin.live = f.request
	ticket := f.ticket(t)
	res := f.respond(t, ticket, "native-allow-once")
	f.clock.advance(11 * time.Second)
	if !f.clock.get().Before(f.request.ExpiresAt) {
		t.Fatal("fixture failed to keep request live")
	}
	if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, gate.ErrExpired) || f.origin.sends != 0 {
		t.Fatal(err)
	}
	e := &fakeExecutor{}
	if err := e.execute(human(), f.request, *res.Decision, admission(f), f.auth); !errors.Is(err, gate.ErrExpired) || e.effects != 0 {
		t.Fatal(err)
	}
}
