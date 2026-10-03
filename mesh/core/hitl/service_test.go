package hitl_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/go-hitl"
	"github.com/hollis-labs/go-hitl/memstore"
)

// fakeClock is a settable clock safe for concurrent use.
type fakeClock struct{ ns atomic.Int64 }

func newClock(t time.Time) *fakeClock    { c := &fakeClock{}; c.Set(t); return c }
func (c *fakeClock) Set(t time.Time)     { c.ns.Store(t.UnixNano()) }
func (c *fakeClock) Now() time.Time      { return time.Unix(0, c.ns.Load()).UTC() }
func (c *fakeClock) Add(d time.Duration) { c.ns.Add(int64(d)) }

var t0 = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func newService(t testing.TB) (*hitl.Service, *fakeClock, *memstore.Store) {
	t.Helper()
	clock := newClock(t0)
	store := memstore.New()
	return hitl.NewService(store, hitl.Options{Clock: clock.Now, PollInterval: 2 * time.Millisecond}), clock, store
}

func request(key string, extra map[string]any) hitl.EnqueueRequest {
	r := hitl.EnqueueRequest{
		ContractVersion: hitl.ContractVersion, Kind: "approval", IdempotencyKey: key,
		Source: hitl.SourceAssertion{ApplicationID: "app-a", AgentID: "agent-1"},
	}
	if len(extra) > 0 {
		r.Extra = map[string]json.RawMessage{}
		for k, v := range extra {
			b, _ := json.Marshal(v)
			r.Extra[k] = b
		}
	}
	return r
}

func withExpiry(r hitl.EnqueueRequest, at time.Time) hitl.EnqueueRequest { r.ExpiresAt = &at; return r }

var caller = hitl.CallerAssertion{ApplicationID: "app-a"}

func respond(itemID, decision string) hitl.RespondCommand {
	return hitl.RespondCommand{
		ItemID:      itemID,
		Response:    hitl.Response{Kind: "approval", Decision: decision},
		Participant: hitl.Participant{Responder: &hitl.Responder{Kind: "human", Ref: "operator"}, Assurance: "asserted"},
	}
}

func mustEnqueue(t testing.TB, s *hitl.Service, r hitl.EnqueueRequest) hitl.Handle {
	t.Helper()
	h, err := s.Enqueue(context.Background(), r)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return h
}

func get(t testing.TB, s *hitl.Service, id string) hitl.RetrievalResult {
	t.Helper()
	res, err := s.Get(context.Background(), hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: id, Caller: caller})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return res
}

func withdrawCmd(id string) hitl.WithdrawCommand {
	return hitl.WithdrawCommand{ContractVersion: hitl.ContractVersion, ItemID: id, Caller: caller}
}

func TestEnqueueIsIdempotentAndDetectsConflicts(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h1 := mustEnqueue(t, s, request("k", map[string]any{"title": "a"}))
	h2 := mustEnqueue(t, s, request("k", map[string]any{"title": "a"}))
	if h1 != h2 || h1.State.IsTerminal() || h1.Revision != 1 {
		t.Fatalf("replay = %+v then %+v", h1, h2)
	}
	_, err := s.Enqueue(ctx, request("k", map[string]any{"title": "b"}))
	var ic *hitl.IdempotencyConflictError
	if !errors.As(err, &ic) || ic.ExistingItemID != h1.ItemID || ic.IdempotencyKey != "k" {
		t.Fatalf("conflict = %v", err)
	}
	other := request("k", map[string]any{"title": "b"})
	other.Source.ApplicationID = "app-b"
	if h3 := mustEnqueue(t, s, other); h3.ItemID == h1.ItemID {
		t.Fatal("the same key in another application must be a distinct item")
	}
	// The digest ignores the idempotency key's own spelling but covers extras and expiry.
	if _, err := s.Enqueue(ctx, withExpiry(request("k", map[string]any{"title": "a"}), t0.Add(time.Hour))); !errors.Is(err, hitl.ErrIdempotencyConflict) {
		t.Fatalf("different expires_at = %v, want conflict", err)
	}
}

func TestEnqueueRejectsInvalidRequests(t *testing.T) {
	s, _, _ := newService(t)
	bad := request("k", nil)
	bad.Source.AgentID = ""
	if _, err := s.Enqueue(context.Background(), bad); !errors.Is(err, hitl.ErrInvalidRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestFirstTerminalWinsAndOutcomeIsImmutable(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	c := respond(h.ItemID, "approved")
	won, err := s.Respond(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if won.OutcomeState() != hitl.StateResolved || won.OutcomeRevision() != 2 {
		t.Fatalf("outcome = %#v", won)
	}
	_, err = s.Respond(ctx, respond(h.ItemID, "denied"))
	var tc *hitl.TerminalConflictError
	if !errors.As(err, &tc) || fmt.Sprint(tc.Outcome) != fmt.Sprint(won) {
		t.Fatalf("second respond = %v", err)
	}
	if _, err := s.Withdraw(ctx, withdrawCmd(h.ItemID)); !errors.As(err, &tc) || fmt.Sprint(tc.Outcome) != fmt.Sprint(won) {
		t.Fatalf("withdraw after resolve = %v", err)
	}
	if got := get(t, s, h.ItemID).Item.TerminalOutcome; fmt.Sprint(got) != fmt.Sprint(won) {
		t.Fatalf("Get outcome changed: %#v", got)
	}
}

func TestWithdrawIsIdempotentAndHonorsExpectedRevision(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	stale := int64(9)
	cmd := withdrawCmd(h.ItemID)
	cmd.ExpectedRevision = &stale
	var se *hitl.StaleRevisionError
	if _, err := s.Withdraw(ctx, cmd); !errors.As(err, &se) || se.Operation != "withdraw" || se.Expected != 9 || se.Actual != 1 || se.Outcome != nil {
		t.Fatalf("stale withdraw = %v", err)
	}
	cmd.ExpectedRevision = nil
	cmd.Reason = "no longer needed"
	first, err := s.Withdraw(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Reason = "different reason"
	again, err := s.Withdraw(ctx, cmd)
	if err != nil || fmt.Sprint(first) != fmt.Sprint(again) {
		t.Fatalf("repeat = %#v, %v; want %#v", again, err, first)
	}
	if c, ok := first.(hitl.Canceled); !ok || c.Cause != hitl.CauseCallerWithdrawn || c.Reason != "no longer needed" {
		t.Fatalf("outcome = %#v", first)
	}
}

func TestRespondValidation(t *testing.T) {
	ctx := context.Background()
	s, _, store := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	for name, mutate := range map[string]func(*hitl.RespondCommand){
		"kind mismatch":  func(c *hitl.RespondCommand) { c.Response.Kind = "attention" },
		"no decision":    func(c *hitl.RespondCommand) { c.Response.Decision = "" },
		"blank note":     func(c *hitl.RespondCommand) { c.Response.Note = "   " },
		"no participant": func(c *hitl.RespondCommand) { c.Participant = hitl.Participant{} },
		"no assurance":   func(c *hitl.RespondCommand) { c.Participant.Assurance = "" },
	} {
		c := respond(h.ItemID, "approved")
		mutate(&c)
		if _, err := s.Respond(ctx, c); !errors.Is(err, hitl.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if got := get(t, s, h.ItemID); got.Item.State.IsTerminal() {
		t.Fatalf("a rejected respond changed the item: %+v", got.Item)
	}
	if _, err := s.Respond(ctx, respond("missing", "approved")); !errors.Is(err, hitl.ErrNotFound) {
		t.Fatalf("missing item = %v", err)
	}
	stale := int64(5)
	c := respond(h.ItemID, "approved")
	c.ExpectedRevision = &stale
	var se *hitl.StaleRevisionError
	if _, err := s.Respond(ctx, c); !errors.As(err, &se) || se.Operation != "resolve" {
		t.Fatalf("stale respond = %v", err)
	}

	// A staged (not yet presented) item is not respondable.
	rec := hitl.Record{ItemID: "staged-1", CallerScope: "app-a", IdempotencyKey: "staged", Digest: "d", Kind: "approval",
		Request: []byte(`{}`), State: hitl.StateStaged, Revision: 3, CreatedAt: t0, UpdatedAt: t0}
	if _, _, err := store.Create(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Respond(ctx, respond("staged-1", "approved")); !errors.Is(err, hitl.ErrInvalidRequest) {
		t.Fatalf("staged respond = %v, want ErrInvalidRequest", err)
	}

	// A profile validator can constrain decisions; core does not.
	strict := hitl.NewService(store, hitl.Options{Clock: func() time.Time { return t0 }, ValidateResponse: func(_ string, r hitl.Response) error {
		if r.Decision != "approved" && r.Decision != "denied" {
			return fmt.Errorf("decision %q is not in the approval profile", r.Decision)
		}
		return nil
	}})
	if _, err := strict.Respond(ctx, respond(h.ItemID, "maybe")); !errors.Is(err, hitl.ErrInvalidRequest) {
		t.Fatalf("profile validator = %v", err)
	}
	if _, err := s.Respond(ctx, respond(h.ItemID, "maybe")); err != nil {
		t.Fatalf("core accepts an open-string decision: %v", err)
	}
}

func TestRespondRecordsResponderAndProofWithoutVerifyingIt(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	c := respond(h.ItemID, "approved")
	c.Participant = hitl.Participant{
		Responder: &hitl.Responder{Kind: "human", Ref: "operator@example.test"},
		Assurance: "authenticated",
		Proof: &hitl.Proof{Scheme: "webauthn", KeyRef: "cred:3f9a", Binds: []hitl.ProofBind{
			{Name: "approval_id", Value: h.ItemID}, {Name: "plan_hash", Value: "sha256:ab12"}, {Name: "nonce", Value: "n-1"},
		}},
	}
	if _, err := s.Respond(ctx, c); err != nil {
		t.Fatal(err)
	}
	res, ok := get(t, s, h.ItemID).Item.TerminalOutcome.(hitl.Resolved)
	if !ok {
		t.Fatal("not resolved")
	}
	got := res.Resolution.Participant
	if got.Proof == nil || got.Proof.Scheme != "webauthn" || len(got.Proof.Binds) != 3 || got.Responder.Ref != "operator@example.test" || got.Assurance != "authenticated" {
		t.Fatalf("participant = %+v", got)
	}
}

func TestCallerIsolation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	other := hitl.CallerAssertion{ApplicationID: "app-b"}
	if _, err := s.Get(ctx, hitl.GetCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: other}); !errors.Is(err, hitl.ErrNotFound) {
		t.Fatalf("Get by another app = %v", err)
	}
	w := withdrawCmd(h.ItemID)
	w.Caller = other
	if _, err := s.Withdraw(ctx, w); !errors.Is(err, hitl.ErrNotFound) {
		t.Fatalf("Withdraw by another app = %v", err)
	}
	if get(t, s, h.ItemID).Item.State.IsTerminal() {
		t.Fatal("another app's withdraw took effect")
	}
}

func TestAwaitWakesWhenTheItemBecomesTerminalAndAbandoningItChangesNothing(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	h := mustEnqueue(t, s, request("k", nil))
	wait := 10000
	cmd := hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller, WaitMs: &wait}

	// Abandoning an await (context canceled) leaves the item untouched.
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.Await(cctx, cmd); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await with expiring ctx = %v", err)
	}
	if get(t, s, h.ItemID).Item.State.IsTerminal() {
		t.Fatal("an abandoned await changed the item")
	}

	done := make(chan hitl.RetrievalResult, 1)
	go func() {
		res, err := s.Await(ctx, cmd)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(20 * time.Millisecond)
	if _, err := s.Respond(ctx, respond(h.ItemID, "approved")); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-done:
		if res.WaitStatus != hitl.WaitTerminal || res.Item.State != hitl.StateResolved || res.Validate() != nil {
			t.Fatalf("await result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await did not wake")
	}

	zero := 0
	h2 := mustEnqueue(t, s, request("k2", nil))
	res, err := s.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h2.ItemID, Caller: caller, WaitMs: &zero})
	if err != nil || res.WaitStatus != hitl.WaitTimeout || res.Validate() != nil {
		t.Fatalf("wait 0 = %+v, %v", res, err)
	}
}

func TestConcurrentRespondAndWithdrawHaveExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	for round := range 50 {
		s, _, _ := newService(t)
		h := mustEnqueue(t, s, request(fmt.Sprintf("k%d", round), nil))
		const n = 12
		var wg sync.WaitGroup
		start := make(chan struct{})
		var wins atomic.Int32
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if i%2 == 0 {
					_, err := s.Respond(ctx, respond(h.ItemID, fmt.Sprintf("d%d", i)))
					switch {
					case err == nil:
						wins.Add(1)
					case !errors.Is(err, hitl.ErrTerminalConflict):
						t.Errorf("respond: %v", err)
					}
					return
				}
				// A repeated withdraw returns the same outcome, so success is fine.
				if _, err := s.Withdraw(ctx, withdrawCmd(h.ItemID)); err != nil && !errors.Is(err, hitl.ErrTerminalConflict) {
					t.Errorf("withdraw: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		final := get(t, s, h.ItemID).Item
		if !final.State.IsTerminal() || final.TerminalOutcome == nil {
			t.Fatalf("round %d: not terminal: %+v", round, final)
		}
		if final.Revision != 2 {
			t.Fatalf("round %d: revision %d, want 2 (one terminal transition)", round, final.Revision)
		}
		if final.State == hitl.StateResolved && wins.Load() != 1 {
			t.Fatalf("round %d: resolved but %d respond calls won", round, wins.Load())
		}
		if final.State == hitl.StateCanceled && wins.Load() != 0 {
			t.Fatalf("round %d: canceled but a respond also reported success", round)
		}
	}
}

// ---- Expiry (D4): a late reply is refused atomically even before any sweeper has run.

func TestRespondAfterExpiresAtIsRefusedBeforeAnySweeperHasRun(t *testing.T) {
	ctx := context.Background()
	s, clock, store := newService(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))

	clock.Set(exp.Add(time.Second)) // past the deadline; ExpireDue is never called
	_, err := s.Respond(ctx, respond(h.ItemID, "approved"))
	var tc *hitl.TerminalConflictError
	if !errors.As(err, &tc) {
		t.Fatalf("late respond = %v, want *TerminalConflictError", err)
	}
	ex, ok := tc.Outcome.(hitl.Expired)
	if !ok || ex.PolicyRef != hitl.ExpiryPolicyRef {
		t.Fatalf("conflict outcome = %#v, want Expired", tc.Outcome)
	}
	rec, _ := store.Get(ctx, h.ItemID)
	if rec.State != hitl.StateExpired || rec.Revision != 2 {
		t.Fatalf("stored record = %+v; the refusal must leave it expired, not pending", rec)
	}
}

func TestRespondExactlyAtExpiresAtIsRefusedAndJustBeforeIsAccepted(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := newService(t)
	exp := t0.Add(time.Minute)
	before := mustEnqueue(t, s, withExpiry(request("before", nil), exp))
	at := mustEnqueue(t, s, withExpiry(request("at", nil), exp))

	clock.Set(exp.Add(-time.Nanosecond))
	out, err := s.Respond(ctx, respond(before.ItemID, "approved"))
	if err != nil {
		t.Fatalf("respond 1ns before expiry: %v", err)
	}
	if !out.OutcomeTime().Before(exp) {
		t.Fatalf("resolved_at %v is not before expires_at %v", out.OutcomeTime(), exp)
	}
	clock.Set(exp)
	if _, err := s.Respond(ctx, respond(at.ItemID, "approved")); !errors.Is(err, hitl.ErrTerminalConflict) {
		t.Fatalf("respond at expires_at = %v, want terminal conflict", err)
	}
}

func TestWithdrawAfterExpiresAtIsRefusedBeforeAnySweeperHasRun(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := newService(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	clock.Set(exp)
	_, err := s.Withdraw(ctx, withdrawCmd(h.ItemID))
	var tc *hitl.TerminalConflictError
	if !errors.As(err, &tc) || tc.Outcome.OutcomeState() != hitl.StateExpired {
		t.Fatalf("late withdraw = %v, want a conflict carrying the expired outcome", err)
	}
}

func TestExpiredItemIsVisibleToGetAndAwaitWithoutASweeper(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := newService(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	if get(t, s, h.ItemID).Item.State.IsTerminal() {
		t.Fatal("expired before its deadline")
	}
	clock.Set(exp)
	res := get(t, s, h.ItemID)
	if res.Item.State != hitl.StateExpired || res.Item.TerminalOutcome == nil {
		t.Fatalf("Get after deadline = %+v", res.Item)
	}
	zero := 0
	aw, err := s.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller, WaitMs: &zero})
	if err != nil || aw.WaitStatus != hitl.WaitTerminal {
		t.Fatalf("Await after deadline = %+v, %v", aw, err)
	}
}

func TestAwaitWakesWhenAnItemExpires(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := newService(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	wait := 10000
	done := make(chan hitl.RetrievalResult, 1)
	go func() {
		res, err := s.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller, WaitMs: &wait})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(20 * time.Millisecond)
	clock.Set(exp)
	select {
	case res := <-done:
		if res.Item.State != hitl.StateExpired || res.WaitStatus != hitl.WaitTerminal {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await did not observe expiry")
	}
}

func TestExpireDueExpiresOnlyLapsedItemsAndIsRepeatable(t *testing.T) {
	ctx := context.Background()
	s, clock, _ := newService(t)
	a := mustEnqueue(t, s, withExpiry(request("a", nil), t0.Add(time.Minute)))
	b := mustEnqueue(t, s, withExpiry(request("b", nil), t0.Add(time.Hour)))
	c := mustEnqueue(t, s, request("c", nil))
	if n, err := s.ExpireDue(ctx); err != nil || n != 0 {
		t.Fatalf("early sweep = %d, %v", n, err)
	}
	clock.Set(t0.Add(2 * time.Minute))
	if n, err := s.ExpireDue(ctx); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1", n, err)
	}
	if n, err := s.ExpireDue(ctx); err != nil || n != 0 {
		t.Fatalf("repeat sweep = %d, %v; want 0", n, err)
	}
	if got := get(t, s, a.ItemID).Item.State; got != hitl.StateExpired {
		t.Fatalf("a = %s", got)
	}
	if get(t, s, b.ItemID).Item.State.IsTerminal() || get(t, s, c.ItemID).Item.State.IsTerminal() {
		t.Fatal("swept an item that had not lapsed")
	}
}

// The core D4 race: many responders and a sweeper collide with the deadline.
// Exactly one terminal outcome may result, and a resolution may only exist if
// it was stamped strictly before expires_at. Run with -race.
func TestConcurrentRespondVersusExpiryHasOneWinnerAndNeverResolvesLate(t *testing.T) {
	ctx := context.Background()
	var resolvedRounds, expiredRounds int
	for round := range 300 {
		s, clock, store := newService(t)
		exp := t0.Add(time.Minute)
		h := mustEnqueue(t, s, withExpiry(request(fmt.Sprintf("k%d", round), nil), exp))
		clock.Set(exp.Add(-time.Millisecond))

		const responders = 6
		var wg sync.WaitGroup
		start := make(chan struct{})
		var okCount atomic.Int32
		var swept atomic.Int32
		for i := range responders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := s.Respond(ctx, respond(h.ItemID, fmt.Sprintf("d%d", i)))
				switch {
				case err == nil:
					okCount.Add(1)
				case !errors.Is(err, hitl.ErrTerminalConflict):
					t.Errorf("respond: %v", err)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			clock.Set(exp) // the deadline arrives concurrently
			n, err := s.ExpireDue(ctx)
			if err != nil {
				t.Errorf("ExpireDue: %v", err)
			}
			swept.Add(int32(n)) //nolint:gosec // n is at most 1 here
		}()
		close(start)
		wg.Wait()

		rec, err := store.Get(ctx, h.ItemID)
		if err != nil {
			t.Fatal(err)
		}
		if !rec.State.IsTerminal() || rec.Revision != 2 {
			t.Fatalf("round %d: record = %+v; want exactly one terminal transition", round, rec)
		}
		switch rec.State {
		case hitl.StateResolved:
			resolvedRounds++
			if okCount.Load() != 1 {
				t.Fatalf("round %d: resolved but %d responders succeeded", round, okCount.Load())
			}
			if swept.Load() != 0 {
				t.Fatalf("round %d: resolved yet the sweeper claims to have expired it", round)
			}
			if at := rec.Outcome.OutcomeTime(); !at.Before(exp) {
				t.Fatalf("round %d: resolved at %v, not before expires_at %v: a late reply was accepted", round, at, exp)
			}
		case hitl.StateExpired:
			expiredRounds++
			if okCount.Load() != 0 {
				t.Fatalf("round %d: expired but a responder also succeeded", round)
			}
			if swept.Load() != 1 {
				// A responder may have materialized the expiry first.
				if swept.Load() != 0 {
					t.Fatalf("round %d: swept = %d", round, swept.Load())
				}
			}
		default:
			t.Fatalf("round %d: unexpected terminal state %s", round, rec.State)
		}
	}
	t.Logf("resolved in %d rounds, expired in %d rounds", resolvedRounds, expiredRounds)
}

func TestConcurrentLateRespondersWithNoSweeperAllLoseToTheSingleExpiry(t *testing.T) {
	ctx := context.Background()
	for round := range 50 {
		s, clock, store := newService(t)
		exp := t0.Add(time.Minute)
		h := mustEnqueue(t, s, withExpiry(request(fmt.Sprintf("k%d", round), nil), exp))
		clock.Set(exp.Add(time.Hour))
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if i%2 == 0 {
					_, err = s.Respond(ctx, respond(h.ItemID, "approved"))
				} else {
					_, err = s.Withdraw(ctx, withdrawCmd(h.ItemID))
				}
				if !errors.Is(err, hitl.ErrTerminalConflict) {
					t.Errorf("late call = %v, want terminal conflict", err)
				}
			}()
		}
		wg.Wait()
		rec, _ := store.Get(ctx, h.ItemID)
		if rec.State != hitl.StateExpired || rec.Revision != 2 {
			t.Fatalf("round %d: record = %+v", round, rec)
		}
	}
}

// ---- Reads never mutate (Get/Await report computed expiry; writes materialize it once).

// spyStore counts writes and can be armed to fail any write.
type spyStore struct {
	hitl.Store
	mu       sync.Mutex
	writes   int
	failOnWr bool
}

func (s *spyStore) arm()       { s.mu.Lock(); s.failOnWr = true; s.mu.Unlock() }
func (s *spyStore) disarm()    { s.mu.Lock(); s.failOnWr = false; s.mu.Unlock() }
func (s *spyStore) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.writes }

func (s *spyStore) write() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.failOnWr {
		return errors.New("spyStore: a write happened where none is allowed")
	}
	return nil
}

func (s *spyStore) Create(ctx context.Context, r hitl.Record) (hitl.Record, bool, error) {
	if err := s.write(); err != nil {
		return hitl.Record{}, false, err
	}
	return s.Store.Create(ctx, r)
}

func (s *spyStore) Swap(ctx context.Context, id string, expected int64, next hitl.Record) error {
	if err := s.write(); err != nil {
		return err
	}
	return s.Store.Swap(ctx, id, expected, next)
}

func spied(t testing.TB) (*hitl.Service, *fakeClock, *spyStore) {
	t.Helper()
	clock := newClock(t0)
	spy := &spyStore{Store: memstore.New()}
	return hitl.NewService(spy, hitl.Options{Clock: clock.Now, PollInterval: 2 * time.Millisecond}), clock, spy
}

func TestGetAndAwaitPastExpiresAtReportExpiredWithoutWriting(t *testing.T) {
	ctx := context.Background()
	s, clock, spy := spied(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	before, _ := spy.Get(ctx, h.ItemID)
	writesAfterEnqueue := spy.count()
	spy.arm() // any write from here on fails the read that caused it

	// Before the deadline: pending, as stored.
	if got := get(t, s, h.ItemID).Item; got.State.IsTerminal() {
		t.Fatalf("expired early: %+v", got)
	}

	clock.Set(exp.Add(time.Hour))
	got := get(t, s, h.ItemID)
	if got.Item.State != hitl.StateExpired || got.Item.Revision != before.Revision+1 || got.Validate() != nil {
		t.Fatalf("Get past deadline = %+v", got.Item)
	}
	ex, ok := got.Item.TerminalOutcome.(hitl.Expired)
	if !ok || ex.PolicyRef != hitl.ExpiryPolicyRef || !ex.TerminatedAt.Equal(exp) || ex.InteractionRevision != got.Item.Revision {
		t.Fatalf("computed outcome = %#v", got.Item.TerminalOutcome)
	}
	for _, wait := range []int{0, 30} {
		aw, err := s.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller, WaitMs: &wait})
		if err != nil || aw.WaitStatus != hitl.WaitTerminal || aw.Item.State != hitl.StateExpired || aw.Validate() != nil {
			t.Fatalf("Await(wait=%d) past deadline = %+v, %v", wait, aw, err)
		}
	}

	after, err := spy.Get(ctx, h.ItemID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != before.State || after.Revision != before.Revision || after.Outcome != nil || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("a read changed the stored record:\n before %+v\n after  %+v", before, after)
	}
	if spy.count() != writesAfterEnqueue {
		t.Fatalf("reads performed %d store writes", spy.count()-writesAfterEnqueue)
	}

	// A subsequent write materializes it exactly once, and the persisted
	// record equals the view the reads reported.
	spy.disarm()
	_, err = s.Respond(ctx, respond(h.ItemID, "approved"))
	if !errors.Is(err, hitl.ErrTerminalConflict) {
		t.Fatalf("late respond = %v", err)
	}
	rec, _ := spy.Get(ctx, h.ItemID)
	if rec.State != hitl.StateExpired || rec.Revision != got.Item.Revision || fmt.Sprint(rec.Outcome) != fmt.Sprint(got.Item.TerminalOutcome) {
		t.Fatalf("materialized %+v differs from the computed view %+v", rec, got.Item)
	}
	if spy.count() != writesAfterEnqueue+1 {
		t.Fatalf("expiry was written %d times, want once", spy.count()-writesAfterEnqueue)
	}
	if n, err := s.ExpireDue(ctx); err != nil || n != 0 {
		t.Fatalf("ExpireDue after materialization = %d, %v; want 0", n, err)
	}
	if spy.count() != writesAfterEnqueue+1 {
		t.Fatal("ExpireDue rewrote an expired record")
	}
}

func TestExpireDueMaterializesAReadOnlyExpiredItemExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s, clock, spy := spied(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	clock.Set(exp)
	viewed := get(t, s, h.ItemID).Item
	base := spy.count()
	if n, err := s.ExpireDue(ctx); err != nil || n != 1 {
		t.Fatalf("ExpireDue = %d, %v; want 1", n, err)
	}
	if n, _ := s.ExpireDue(ctx); n != 0 {
		t.Fatalf("second ExpireDue = %d", n)
	}
	if spy.count() != base+1 {
		t.Fatalf("writes = %d, want exactly one", spy.count()-base)
	}
	rec, _ := spy.Get(ctx, h.ItemID)
	if rec.Revision != viewed.Revision || fmt.Sprint(rec.Outcome) != fmt.Sprint(viewed.TerminalOutcome) {
		t.Fatalf("stored %+v != previously computed view %+v", rec, viewed)
	}
}

func TestAwaitWhoseDeadlinePassesWhileWaitingReturnsComputedExpiryWithoutWriting(t *testing.T) {
	ctx := context.Background()
	s, clock, spy := spied(t)
	exp := t0.Add(time.Minute)
	h := mustEnqueue(t, s, withExpiry(request("k", nil), exp))
	base := spy.count()
	spy.arm()
	wait := 10000
	done := make(chan hitl.RetrievalResult, 1)
	go func() {
		res, err := s.Await(ctx, hitl.AwaitCommand{ContractVersion: hitl.ContractVersion, ItemID: h.ItemID, Caller: caller, WaitMs: &wait})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	time.Sleep(20 * time.Millisecond)
	clock.Set(exp)
	select {
	case res := <-done:
		if res.WaitStatus != hitl.WaitTerminal || res.Item.State != hitl.StateExpired {
			t.Fatalf("result = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await did not return")
	}
	rec, _ := spy.Get(ctx, h.ItemID)
	if rec.State.IsTerminal() || rec.Revision != 1 || spy.count() != base {
		t.Fatalf("Await mutated the store: %+v, writes %d", rec, spy.count()-base)
	}
}

func TestEnqueueReplayOfALapsedItemDoesNotWrite(t *testing.T) {
	s, clock, spy := spied(t)
	exp := t0.Add(time.Minute)
	req := withExpiry(request("k", nil), exp)
	mustEnqueue(t, s, req)
	base := spy.count()
	clock.Set(exp)
	h := mustEnqueue(t, s, req)
	if h.State != hitl.StateExpired {
		t.Fatalf("replay handle = %+v, want the computed expired state", h)
	}
	if spy.count() != base+1 { // the replay's Create attempt is the only store call that may write
		t.Fatalf("replay wrote %d times", spy.count()-base)
	}
}
