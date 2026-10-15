package guard

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func tripped(t *testing.T, clk *fakeClock, cooldown time.Duration) *CircuitBreaker {
	t.Helper()
	cb := NewCircuitBreaker(BreakerConfig{Threshold: 1, Cooldown: cooldown, Clock: clk})
	if !cb.RecordFailure() {
		t.Fatal("expected the first failure to trip a threshold-1 breaker")
	}
	return cb
}

func TestBreakerDefaults(t *testing.T) {
	cb := NewCircuitBreaker(BreakerConfig{})
	for i := 1; i < DefaultThreshold; i++ {
		if cb.RecordFailure() {
			t.Fatalf("tripped after %d failures, want %d", i, DefaultThreshold)
		}
	}
	if !cb.RecordFailure() || cb.State() != CircuitOpen {
		t.Fatalf("expected a trip at DefaultThreshold=%d", DefaultThreshold)
	}
	if got := cb.RetryAfter(); got <= 0 || got > DefaultCooldown {
		t.Fatalf("RetryAfter = %v, want within (0, %v]", got, DefaultCooldown)
	}
}

func TestBreakerTripsOnConsecutiveFailuresOnly(t *testing.T) {
	cb := NewCircuitBreaker(BreakerConfig{Threshold: 3, Clock: newFakeClock()})
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitClosed {
		t.Fatal("a success must reset the consecutive count")
	}
	if !cb.RecordFailure() {
		t.Fatal("expected a trip on the third consecutive failure")
	}
	if cb.RecordFailure() {
		t.Fatal("a failure while open must not report a fresh trip")
	}
}

func TestBreakerRefusesDuringCooldown(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, 10*time.Second)
	clk.advance(9 * time.Second)
	if cb.Allow() {
		t.Fatal("admitted during cooldown")
	}
	if got := cb.RetryAfter(); got != time.Second {
		t.Fatalf("RetryAfter = %v, want 1s", got)
	}
	// A late failure while open does not restart the cooldown.
	cb.RecordFailure()
	clk.advance(time.Second)
	if _, ok := cb.Admit(); !ok {
		t.Fatal("expected the probe once the original cooldown elapsed")
	}
}

// TestBreakerHalfOpenSingleProbeConcurrent is the conformance test for the
// half-open contract: however many callers arrive at once, exactly one is
// admitted, and nobody else is until it reports.
func TestBreakerHalfOpenSingleProbeConcurrent(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Second)

	const goroutines, callsEach = 64, 32
	var (
		admitted atomic.Int64
		probe    atomic.Value
		wg       sync.WaitGroup
		start    = make(chan struct{})
	)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range callsEach {
				if a, ok := cb.Admit(); ok {
					admitted.Add(1)
					probe.Store(a)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := admitted.Load(); n != 1 {
		t.Fatalf("half-open admitted %d of %d calls, want exactly 1", n, goroutines*callsEach)
	}
	a := probe.Load().(Admission)
	if !a.Probe() || cb.State() != CircuitHalfOpen {
		t.Fatal("the admitted call must be the half-open probe")
	}
	a.Success()
	if cb.State() != CircuitClosed || !cb.Allow() || !cb.Allow() {
		t.Fatal("a successful probe must close the circuit for everyone")
	}
}

func TestBreakerFailedProbeReopens(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Second)
	a, ok := cb.Admit()
	if !ok || !a.Probe() {
		t.Fatal("expected a probe")
	}
	if a.Failure() {
		t.Fatal("a failed probe re-opens but does not report a fresh trip")
	}
	if cb.State() != CircuitOpen || cb.RetryAfter() != time.Second {
		t.Fatalf("state=%v retry=%v, want open with a restarted cooldown", cb.State(), cb.RetryAfter())
	}
}

func TestBreakerReleasedProbeFreesSlot(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Second)
	a, _ := cb.Admit()
	if cb.Allow() {
		t.Fatal("second caller admitted while the probe holds the slot")
	}
	a.Release()
	b, ok := cb.Admit()
	if !ok || !b.Probe() {
		t.Fatal("after Release the next caller must become the probe")
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatal("Release must not change the state")
	}
}

func TestBreakerProbeTimeoutReclaimsSlot(t *testing.T) {
	clk := newFakeClock()
	cb := NewCircuitBreaker(BreakerConfig{Threshold: 1, Cooldown: time.Minute, ProbeTimeout: 5 * time.Second, Clock: clk})
	cb.RecordFailure()
	clk.advance(time.Minute)
	stale, _ := cb.Admit()
	clk.advance(4 * time.Second)
	if cb.Allow() {
		t.Fatal("slot reclaimed before ProbeTimeout")
	}
	if got := cb.RetryAfter(); got != time.Second {
		t.Fatalf("RetryAfter = %v, want the rest of the probe lease (1s)", got)
	}
	clk.advance(time.Second)
	fresh, ok := cb.Admit()
	if !ok || !fresh.Probe() {
		t.Fatal("expected the slot to be reclaimed after ProbeTimeout")
	}
	// The abandoned probe's late success must not close the circuit.
	stale.Success()
	if cb.State() != CircuitHalfOpen {
		t.Fatal("a reclaimed probe's late result must be ignored")
	}
	fresh.Failure()
	if cb.State() != CircuitOpen {
		t.Fatal("the current probe's failure must re-open the circuit")
	}
}

func TestBreakerStaleAdmissionIgnoredAfterTrip(t *testing.T) {
	clk := newFakeClock()
	cb := NewCircuitBreaker(BreakerConfig{Threshold: 2, Cooldown: time.Second, Clock: clk})
	slow, _ := cb.Admit() // admitted while closed
	cb.RecordFailure()
	cb.RecordFailure() // trips
	slow.Success()     // arrives late
	if cb.State() != CircuitOpen {
		t.Fatal("a call admitted before the trip must not close the circuit")
	}
}

func TestBreakerResetIgnoresInFlightProbe(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Second)
	a, _ := cb.Admit()
	cb.Reset()
	if cb.State() != CircuitClosed {
		t.Fatal("Reset must close the circuit")
	}
	a.Failure()
	if cb.State() != CircuitClosed {
		t.Fatal("a probe admitted before Reset must not re-open the circuit")
	}
}

func TestBreakerStateHasNoSideEffect(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Hour)
	for range 3 {
		if cb.State() != CircuitOpen || cb.RetryAfter() != 0 {
			t.Fatal("State and RetryAfter must not move an elapsed open circuit to half-open")
		}
	}
}

func TestZeroAdmissionIsInert(t *testing.T) {
	var a Admission
	a.Success()
	a.Release()
	if a.Failure() || a.Probe() {
		t.Fatal("the zero Admission must do nothing")
	}
}

func TestCircuitStateString(t *testing.T) {
	for s, want := range map[CircuitState]string{CircuitClosed: "closed", CircuitOpen: "open", CircuitHalfOpen: "half-open", 9: "unknown"} {
		if got := s.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", s, got, want)
		}
	}
}

func TestBreakerAllowRelease(t *testing.T) {
	clk := newFakeClock()
	cb := tripped(t, clk, time.Second)
	clk.advance(time.Second)
	if !cb.Allow() || cb.Allow() {
		t.Fatal("expected exactly one probe from Allow")
	}
	cb.Release()
	if !cb.Allow() {
		t.Fatal("Release must free the probe slot for the next Allow")
	}
	cb.RecordSuccess()
	cb.Release() // no-op while closed
	if cb.State() != CircuitClosed {
		t.Fatal("Release must do nothing outside half-open")
	}
}
