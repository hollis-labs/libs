package guard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestGuard(clk *fakeClock, quota QuotaCheck) *Guard {
	return New(Config{
		Breaker:  BreakerConfig{Threshold: 2, Cooldown: 10 * time.Second},
		Cooldown: CooldownConfig{Backoff: noJitter(), AuthCooldown: time.Minute},
		Quota:    quota,
		Clock:    clk,
	})
}

func fail(err error) func(context.Context) error {
	return func(context.Context) error { return err }
}

func ok(context.Context) error { return nil }

func refusal(t *testing.T, err error) Decision {
	t.Helper()
	var re *RefusedError
	if !errors.As(err, &re) || !errors.Is(err, ErrRefused) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	return re.Decision
}

// A malformed request must not cool down, or count against, a healthy account.
func TestGuardRequestErrorLeavesAccountHealthy(t *testing.T) {
	g := newTestGuard(newFakeClock(), nil)
	bad := HTTPError(http.StatusBadRequest, nil, errors.New("malformed"))
	for range 10 {
		if err := g.Do(context.Background(), keyA1, fail(bad)); !errors.Is(err, bad) {
			t.Fatalf("fn's error must come back unchanged, got %v", err)
		}
	}
	if d := g.Check(keyA1); !d.Allowed {
		t.Fatalf("request-scoped errors refused the account: %+v", d)
	}
	if g.State(keyA1) != CircuitClosed {
		t.Fatal("request-scoped errors tripped the breaker")
	}
}

func TestGuardQuotaCoolsKeyWithRetryAfter(t *testing.T) {
	clk := newFakeClock()
	g := newTestGuard(clk, nil)
	e := HTTPError(http.StatusTooManyRequests, http.Header{"Retry-After": []string{"20"}}, nil)
	_ = g.Do(context.Background(), keyA1, fail(e))

	d := refusal(t, g.Do(context.Background(), keyA1, ok))
	if d.Reason != ReasonCooldown || d.Class != ClassQuota || d.RetryAfter != 20*time.Second {
		t.Fatalf("decision = %+v, want quota cooldown for 20s", d)
	}
	if !g.Check(keyA2).Allowed || !g.Check(keyB1).Allowed {
		t.Fatal("a quota cooldown must not spill onto other models or accounts")
	}
	if g.State(keyA1) != CircuitClosed {
		t.Fatal("quota errors must not count against the breaker")
	}
	clk.advance(20 * time.Second)
	if err := g.Do(context.Background(), keyA1, ok); err != nil {
		t.Fatalf("expected admission after the retry-after, got %v", err)
	}
}

func TestGuardAuthCoolsAccount(t *testing.T) {
	g := newTestGuard(newFakeClock(), nil)
	_ = g.Do(context.Background(), keyA1, fail(HTTPError(http.StatusUnauthorized, nil, nil)))
	d := g.Check(keyA2)
	if d.Allowed || d.Reason != ReasonCooldown || d.Class != ClassAuth || d.RetryAfter != time.Minute {
		t.Fatalf("decision for another model of the account = %+v", d)
	}
	if !g.Check(keyB1).Allowed {
		t.Fatal("auth failure of one account must not refuse another")
	}
}

func TestGuardTransientTripsBreakerAndCoolsDown(t *testing.T) {
	clk := newFakeClock()
	g := newTestGuard(clk, nil)
	five := HTTPError(http.StatusServiceUnavailable, nil, nil)
	_ = g.Do(context.Background(), keyA1, fail(five))
	if d := g.Check(keyA1); d.Allowed || d.Reason != ReasonCooldown || d.RetryAfter != time.Second {
		t.Fatalf("after one 503: %+v, want a 1s backoff", d)
	}
	clk.advance(time.Second)
	_ = g.Do(context.Background(), keyA1, fail(five))
	if g.State(keyA1) != CircuitOpen {
		t.Fatal("two consecutive transient failures must trip a threshold-2 breaker")
	}
	clk.advance(2 * time.Second) // backoff over; breaker still open
	d := g.Check(keyA2)          // same account, other model: same breaker
	if d.Allowed || d.Reason != ReasonCircuitOpen || d.RetryAfter != 8*time.Second {
		t.Fatalf("decision = %+v, want circuit open for the 8s left of its 10s cooldown", d)
	}
	if !g.Check(keyB1).Allowed {
		t.Fatal("another account's breaker must be independent")
	}
}

func TestGuardConnectionErrorsTripWithoutCooldown(t *testing.T) {
	g := newTestGuard(newFakeClock(), nil)
	conn := &Error{Class: ClassConnection}
	_ = g.Do(context.Background(), keyA1, fail(conn))
	if !g.Check(keyA1).Allowed {
		t.Fatal("one connection error must set no cooldown")
	}
	_ = g.Do(context.Background(), keyA1, fail(conn))
	if d := g.Check(keyA1); d.Reason != ReasonCircuitOpen {
		t.Fatalf("connection errors must count against the breaker: %+v", d)
	}
}

func TestGuardHalfOpenSingleProbe(t *testing.T) {
	clk := newFakeClock()
	g := newTestGuard(clk, nil)
	conn := &Error{Class: ClassConnection}
	_ = g.Do(context.Background(), keyA1, fail(conn))
	_ = g.Do(context.Background(), keyA1, fail(conn))
	clk.advance(10 * time.Second)

	const callers = 64
	var (
		admitted atomic.Int64
		refused  atomic.Int64
		wg       sync.WaitGroup
		start    = make(chan struct{})
		release  = make(chan struct{})
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := g.Do(context.Background(), keyA1, func(context.Context) error {
				admitted.Add(1)
				<-release
				return nil
			})
			if errors.Is(err, ErrRefused) {
				refused.Add(1)
			}
		}()
	}
	close(start)
	// Hold the probe until every other caller has been turned away, so none
	// can arrive after the probe legitimately closes the circuit.
	for refused.Load() < callers-1 && admitted.Load() < 2 {
		time.Sleep(time.Millisecond)
	}
	if n := admitted.Load(); n != 1 {
		close(release)
		wg.Wait()
		t.Fatalf("half-open guard admitted %d concurrent calls, want 1", n)
	}
	if d := g.Check(keyA1); d.Reason != ReasonProbeInFlight {
		t.Fatalf("while the probe is outstanding Check = %+v, want probe-in-flight", d)
	}
	close(release)
	wg.Wait()
	if n, r := admitted.Load(), refused.Load(); n != 1 || r != callers-1 {
		t.Fatalf("admitted %d, refused %d; want 1 and %d", n, r, callers-1)
	}
	if g.State(keyA1) != CircuitClosed {
		t.Fatal("the probe's success must close the breaker")
	}
}

func TestGuardCanceledAndPanicFreeProbe(t *testing.T) {
	clk := newFakeClock()
	g := newTestGuard(clk, nil)
	conn := &Error{Class: ClassConnection}
	_ = g.Do(context.Background(), keyA1, fail(conn))
	_ = g.Do(context.Background(), keyA1, fail(conn))
	clk.advance(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Do(ctx, keyA1, func(ctx context.Context) error { return ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if g.State(keyA1) != CircuitHalfOpen {
		t.Fatal("a canceled probe must record no verdict")
	}

	func() {
		defer func() { _ = recover() }()
		_ = g.Do(context.Background(), keyA1, func(context.Context) error { panic("boom") })
	}()
	if g.State(keyA1) != CircuitHalfOpen {
		t.Fatal("a panicking probe must record no verdict")
	}
	if err := g.Do(context.Background(), keyA1, ok); err != nil {
		t.Fatalf("the probe slot must be free after cancel and panic, got %v", err)
	}
	if g.State(keyA1) != CircuitClosed {
		t.Fatal("expected the next probe to close the breaker")
	}
}

func TestGuardQuotaCheckCombines(t *testing.T) {
	clk := newFakeClock()
	var remaining atomic.Int64
	remaining.Store(0)
	g := newTestGuard(clk, func(Key) Decision {
		if remaining.Load() > 0 {
			return Decision{Allowed: true}
		}
		return Decision{RetryAfter: 30 * time.Second}
	})
	d := refusal(t, g.Do(context.Background(), keyA1, ok))
	if d.Reason != ReasonQuota || d.RetryAfter != 30*time.Second {
		t.Fatalf("decision = %+v, want quota refusal for 30s", d)
	}
	if g.State(keyA1) != CircuitClosed {
		t.Fatal("a quota refusal must not touch the breaker")
	}

	// Cooldown and quota both refuse: cooldown is the reason, the longer wait wins.
	_, _ = g.Cooldown().Record(keyA1, ClassQuota, 5*time.Second)
	d = g.Check(keyA1)
	if d.Reason != ReasonCooldown || d.RetryAfter != 30*time.Second {
		t.Fatalf("decision = %+v, want cooldown reason with the 30s wait", d)
	}

	remaining.Store(1)
	clk.advance(5 * time.Second)
	if err := g.Do(context.Background(), keyA1, ok); err != nil {
		t.Fatalf("expected admission, got %v", err)
	}
}

func TestGuardCheckHasNoSideEffect(t *testing.T) {
	clk := newFakeClock()
	g := newTestGuard(clk, nil)
	conn := &Error{Class: ClassConnection}
	_ = g.Do(context.Background(), keyA1, fail(conn))
	_ = g.Do(context.Background(), keyA1, fail(conn))
	clk.advance(10 * time.Second)
	for range 3 {
		if !g.Check(keyA1).Allowed {
			t.Fatal("Check must report that the next call may probe")
		}
	}
	if g.State(keyA1) != CircuitOpen {
		t.Fatal("Check must not take the probe slot")
	}
}

func TestGuardTicketAPIAndReset(t *testing.T) {
	g := newTestGuard(newFakeClock(), nil)
	tk, d := g.Admit(keyA1)
	if !d.Allowed || tk.Probe() {
		t.Fatalf("closed guard: %+v", d)
	}
	if c := tk.Done(fmt.Errorf("wrapped: %w", &Error{Class: ClassQuota, RetryAfter: time.Minute})); c != ClassQuota {
		t.Fatalf("Done returned %v", c)
	}
	if g.Check(keyA1).Allowed {
		t.Fatal("expected the quota cooldown")
	}
	g.Reset(keyA1)
	if !g.Check(keyA1).Allowed {
		t.Fatal("Reset must clear the key's cooldown")
	}
	tk2, _ := g.Admit(keyA1)
	tk2.Release()
	var zero Ticket
	zero.Release()
	if zero.Done(errors.New("x")) != ClassUnknown {
		t.Fatal("the zero Ticket must be inert")
	}
}

func TestDoValue(t *testing.T) {
	g := newTestGuard(newFakeClock(), nil)
	v, err := DoValue(context.Background(), g, keyA1, func(context.Context) (int, error) { return 7, nil })
	if v != 7 || err != nil {
		t.Fatalf("DoValue = (%d, %v)", v, err)
	}
	_, _ = g.Cooldown().Record(keyA1, ClassQuota, time.Minute)
	v, err = DoValue(context.Background(), g, keyA1, func(context.Context) (int, error) { return 7, nil })
	if v != 0 || !errors.Is(err, ErrRefused) {
		t.Fatalf("refused DoValue = (%d, %v)", v, err)
	}
	if got := err.Error(); got != "guard: refused (cooldown, quota, retry after 1m0s)" {
		t.Fatalf("Error() = %q", got)
	}
}
