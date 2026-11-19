package gatecrasher

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
)

func TestCrashBeforeReservationCreatesNoInteractionAndCanRetry(t *testing.T) {
	f := newFixture()
	f.journal.failReserve = true
	if _, err := f.c.Request(human(), f.request); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.restart()
	ticket := f.ticket(t)
	pending, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || pending.State != gate.Pending {
		t.Fatal(pending, err)
	}
}

func TestCrashAfterEnqueueBeforeMappingRehydratesTerminalOwnerHandle(t *testing.T) {
	f := newFixture()
	f.journal.failSwap = true
	if _, err := f.c.Request(human(), f.request); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	// Simulate the existing owner's authenticated presentation resolving while
	// coordinator is down. No coordinator map or second decision is available.
	req, err := f.c.enqueue(f.request)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.service.Enqueue(human(), req)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.auth.Authorize(human(), f.request, gate.Ticket{AuthorityRef: "fake-owner"}, ResponseAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.Respond(human(), hitl.RespondCommand{ItemID: h.ItemID, ExpectedRevision: &h.Revision, Response: hitl.Response{Kind: InteractionKind, Decision: "native-allow-once"}, Participant: p})
	if err != nil {
		t.Fatal(err)
	}
	f.restart()
	tickets, next, err := f.c.Recover(human(), "", 10)
	if err != nil || len(tickets) != 1 || next != "" {
		t.Fatal(tickets, next, err)
	}
	ticket := tickets[0]
	if ticket.Handle.ItemID != h.ItemID || ticket.Handle.Revision != outcome.OutcomeRevision() || ticket.Handle.State != hitl.StateResolved {
		t.Fatal("did not retain actual replay handle", ticket)
	}
	res, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || res.State != gate.Decided || res.Decision.Resolution.ResolutionID != outcome.(hitl.Resolved).Resolution.ResolutionID {
		t.Fatal(res, err)
	}
	if _, err = f.c.Deliver(human(), ticket); err != nil {
		t.Fatal("terminal replay handle refused", err)
	}
	same, err := f.c.Request(human(), f.request)
	if err != nil || !reflect.DeepEqual(ticket, same) {
		t.Fatal(same, err)
	}
}

func TestCrashAfterDecisionBeforeSendReloadsAuthoritativeDecision(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	res := f.respond(t, ticket, "native-allow-once")
	f.restart()
	again, err := f.c.Await(human(), ticket, gate.WaitOptions{})
	if err != nil || !reflect.DeepEqual(res, again) {
		t.Fatal(again, err)
	}
	if _, err = f.c.Deliver(human(), ticket); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Deliver(human(), ticket); err != nil || f.origin.sends != 1 {
		t.Fatal(err, f.origin.sends)
	}
}

func TestLostAcknowledgementReconcilesWithoutBlindResend(t *testing.T) {
	for _, mode := range []string{"lost", "sent", "crash-before-send"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			ticket := f.ticket(t)
			f.respond(t, ticket, "native-allow-once")
			f.origin.mode = mode
			p, err := f.c.Deliver(human(), ticket)
			if !errors.Is(err, ErrUnknown) || p.Status != gate.DeliveryUnknown && p.Status != gate.DeliverySent {
				t.Fatal(p, err)
			}
			f.restart()
			f.origin.reconciliation = "unknown"
			if _, err = f.c.Deliver(human(), ticket); !errors.Is(err, ErrUnknown) || f.origin.sends != 1 {
				t.Fatal("unknown blindly resent", err, f.origin.sends)
			}
			f.origin.reconciliation = "auto"
			f.origin.mode = "ack"
			p, err = f.c.Deliver(human(), ticket)
			if err != nil || p.Status != gate.DeliveryAcknowledged {
				t.Fatal(p, err)
			}
			want := 1
			if mode == "crash-before-send" {
				want = 2
			}
			if f.origin.sends != want {
				t.Fatal("reconciliation sent unexpected native reply", f.origin.sends)
			}
		})
	}
}

func TestCrashAfterPhysicalSendBeforeJournalCommitLeavesUnknownLease(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.respond(t, ticket, "native-allow-once")
	f.origin.afterSend = func() { f.journal.mu.Lock(); f.journal.failSwap = true; f.journal.mu.Unlock() }
	if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.restart()
	if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, ErrBusy) {
		t.Fatal("abandoned lease bypassed", err)
	}
	f.clock.advance(2 * time.Second)
	if p, err := f.c.Deliver(human(), ticket); err != nil || p.Status != gate.DeliveryAcknowledged || f.origin.sends != 1 {
		t.Fatal(p, err, f.origin.sends)
	}
}

func TestUnknownReconciliationMustMatchExactNativeIDAndGeneration(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.respond(t, ticket, "native-allow-once")
	f.origin.mode = "lost"
	_, _ = f.c.Deliver(human(), ticket)
	f.origin.reconciliation = "wrong-id"
	if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, gate.ErrBinding) || f.origin.sends != 1 {
		t.Fatal(err)
	}
	f.origin.live.Binding.Generation = 8
	f.origin.reconciliation = "unknown"
	if _, err := f.c.Deliver(human(), ticket); !errors.Is(err, ErrUnknown) || f.origin.sends != 1 {
		t.Fatal(err)
	}
}

func TestDeliveryLeaseFencesConcurrentWorkers(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.respond(t, ticket, "native-allow-once")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := f.c.Deliver(human(), ticket); errs <- err }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrCAS) && !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	if f.origin.sends != 1 {
		t.Fatal("concurrent workers sent redundant reply", f.origin.sends)
	}
}

func TestEffectThenCrashRemainsUnknownUntilHostReconciles(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	res := f.respond(t, ticket, "native-allow-once")
	p, err := f.c.Deliver(human(), ticket)
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	if err = executor.execute(human(), f.request, *res.Decision, admission(f), f.auth); err != nil {
		t.Fatal(err)
	}
	unknown := gate.ExecutionReceipt{DecisionRef: p.DecisionRef, OperationID: f.request.OperationID, Binding: f.request.Binding, State: gate.ExecutionUnknown}
	if err = f.c.RecordExecution(human(), unknown); err != nil {
		t.Fatal(err)
	}
	f.restart()
	if _, err = f.c.Deliver(human(), ticket); err != nil {
		t.Fatal(err)
	}
	if executor.effects != 1 {
		t.Fatal("delivery caused execution")
	}
	if err = executor.execute(human(), f.request, *res.Decision, admission(f), f.auth); !errors.Is(err, gate.ErrDenied) || executor.effects != 1 {
		t.Fatal("consumed grant replayed effect", err)
	}
	report := unknown
	report.State = gate.ExecutionSucceeded
	report.EffectRef = "fake-effect-1"
	if err = f.c.RecordExecution(human(), report); !errors.Is(err, ErrUnknown) {
		t.Fatal("unreconciled unknown changed", err)
	}
	report.EvidenceRef = "fake-host-effect-query"
	if err = f.c.RecordExecution(human(), report); err != nil {
		t.Fatal(err)
	}
	report.State = gate.ExecutionFailed
	if err = f.c.RecordExecution(human(), report); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal effect report overwritten", err)
	}
}

func TestHistoricalDeliveryAfterExpiryOrReplacementDoesNotAuthorizeNewSend(t *testing.T) {
	for _, mode := range []string{"ack", "lost", "crash-before-send"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture()
			ticket := f.ticket(t)
			f.respond(t, ticket, "native-allow-once")
			f.origin.mode = mode
			_, _ = f.c.Deliver(human(), ticket)
			f.clock.advance(time.Minute)
			f.origin.live.Binding.Generation = 8
			f.auth.deny = true
			f.restart()
			p, err := f.c.Deliver(human(), ticket)
			if mode == "crash-before-send" {
				if !errors.Is(err, ErrUnknown) || p.Status != gate.DeliveryUnknown {
					t.Fatal(p, err)
				}
			} else if err != nil || p.Status != gate.DeliveryAcknowledged {
				t.Fatal("historical acknowledgement hidden by current expiry/authority", p, err)
			}
			if f.origin.sends != 1 {
				t.Fatal("historical lookup sent a new grant", f.origin.sends)
			}
		})
	}
}

func TestBlockedSenderLeaseReclaimUsesRecipientFencing(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.respond(t, ticket, "native-allow-once")
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	// One old sender is paused after preflight. The recipient handles competing
	// replies idempotently and claims the effect under its own admission fence.
	f.origin.beforeWrite = func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	executor := &fakeExecutor{}
	f.origin.fence = func(r gate.Request, res gate.Resolution) error {
		return executor.execute(human(), r, *res.Decision, admission(f), f.auth)
	}
	done := make(chan error, 1)
	go func() { _, err := f.c.Deliver(human(), ticket); done <- err }()
	<-entered
	f.clock.advance(2 * time.Second)
	// Reconciliation observes no accepted reply. A real adapter must also fence
	// the previously in-flight write. This fake's accepted map makes that write
	// idempotent when it eventually arrives.
	p, err := f.c.Deliver(human(), ticket)
	if err != nil || p.Status != gate.DeliveryAcknowledged {
		t.Fatal(p, err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrCAS) {
		t.Fatal("old sender updated reclaimed journal lease", err)
	}
	if f.origin.sends != 1 || executor.effects != 1 {
		t.Fatal("recipient fencing failed", f.origin.sends, executor.effects)
	}
}

func TestBlockedSenderRechecksAuthorityBeforePhysicalAllow(t *testing.T) {
	f := newFixture()
	ticket := f.ticket(t)
	f.respond(t, ticket, "native-allow-once")
	entered := make(chan struct{})
	release := make(chan struct{})
	f.origin.beforeWrite = func() { close(entered); <-release }
	executor := &fakeExecutor{}
	f.origin.fence = func(r gate.Request, res gate.Resolution) error {
		return executor.execute(human(), r, *res.Decision, admission(f), f.auth)
	}
	done := make(chan error, 1)
	go func() { _, err := f.c.Deliver(human(), ticket); done <- err }()
	<-entered
	f.auth.deny = true
	close(release)
	if err := <-done; !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	if f.origin.sends != 0 || executor.effects != 0 {
		t.Fatal("revoked authority emitted allow", f.origin.sends, executor.effects)
	}
}

type enqueueSpy struct {
	InteractionPort
	calls int
}

func (p *enqueueSpy) Enqueue(ctx context.Context, r hitl.EnqueueRequest) (hitl.Handle, error) {
	p.calls++
	return p.InteractionPort.Enqueue(ctx, r)
}

func TestUnattachedIntentPinsOwnerAndCallerBeforeExternalEnqueue(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.OwnerRef = "replacement-owner" }, func(c *Config) { c.CallerScope = "replacement-scope" }} {
		f := newFixture()
		f.journal.failSwap = true
		if _, err := f.c.Request(human(), f.request); !errors.Is(err, errCrash) {
			t.Fatal(err)
		}
		f.restart()
		spy := &enqueueSpy{InteractionPort: f.service}
		f.c.cfg.Interactions = spy
		change(&f.c.cfg)
		if _, _, err := f.c.Recover(human(), "", 10); !errors.Is(err, gate.ErrBinding) {
			t.Fatal(err)
		}
		if spy.calls != 0 {
			t.Fatal("recovery re-enqueued into a different owner/scope")
		}
	}
}
