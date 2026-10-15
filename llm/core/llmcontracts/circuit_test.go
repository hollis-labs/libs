package llmcontracts

import (
	"sync"
	"testing"
	"time"
)

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3)

	if cb.IsOpen() {
		t.Fatal("expected circuit to start closed")
	}

	// First two failures should not trip.
	if tripped := cb.RecordFailure(); tripped {
		t.Fatal("should not trip on first failure")
	}
	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after 1 failure")
	}

	if tripped := cb.RecordFailure(); tripped {
		t.Fatal("should not trip on second failure")
	}
	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after 2 failures")
	}

	// Third failure should trip.
	if tripped := cb.RecordFailure(); !tripped {
		t.Fatal("expected circuit to trip on third failure")
	}
	if !cb.IsOpen() {
		t.Fatal("expected circuit to be open after threshold")
	}
}

func TestCircuitBreaker_SuccessResetsCounter(t *testing.T) {
	cb := NewCircuitBreaker(3)

	cb.RecordFailure()
	cb.RecordFailure()
	// Two failures, then a success should reset.
	cb.RecordSuccess()

	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after success")
	}

	// Now need 3 more failures to trip.
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after 2 new failures")
	}

	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Fatal("expected circuit to trip after 3 consecutive failures")
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(3)

	// Trip the circuit.
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Fatal("expected circuit to be open")
	}

	// Reset should close it.
	cb.Reset()
	if cb.IsOpen() {
		t.Fatal("expected circuit to be closed after reset")
	}

	// Should need full threshold again to trip.
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after 2 failures post-reset")
	}
}

func TestCircuitBreaker_SuccessClosesOpenCircuit(t *testing.T) {
	cb := NewCircuitBreaker(2)

	cb.RecordFailure()
	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Fatal("expected circuit to be open")
	}

	cb.RecordSuccess()
	if cb.IsOpen() {
		t.Fatal("expected success to close open circuit")
	}
}

func TestCircuitBreaker_DefaultThreshold(t *testing.T) {
	cb := NewCircuitBreaker(0)

	// Default threshold should be 3.
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.IsOpen() {
		t.Fatal("expected circuit to remain closed after 2 failures with default threshold")
	}

	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Fatal("expected circuit to trip at default threshold of 3")
	}
}

func TestCircuitBreaker_FurtherFailuresAfterTrip(t *testing.T) {
	cb := NewCircuitBreaker(2)

	// Trip it.
	cb.RecordFailure()
	tripped := cb.RecordFailure()
	if !tripped {
		t.Fatal("expected trip on second failure")
	}

	// Further failures should not report tripping again.
	tripped = cb.RecordFailure()
	if tripped {
		t.Fatal("should not report tripping again when already open")
	}
	if !cb.IsOpen() {
		t.Fatal("circuit should still be open")
	}
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(100)

	var wg sync.WaitGroup
	// Run 100 goroutines recording failures concurrently.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordFailure()
		}()
	}
	wg.Wait()

	if !cb.IsOpen() {
		t.Fatal("expected circuit to be open after 100 concurrent failures with threshold 100")
	}

	cb.Reset()
	if cb.IsOpen() {
		t.Fatal("expected circuit to be closed after reset")
	}
}

func TestCircuitBreaker_State(t *testing.T) {
	cb := NewCircuitBreaker(1)

	if cb.State() != CircuitClosed {
		t.Fatalf("expected CircuitClosed, got %d", cb.State())
	}

	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Fatalf("expected CircuitOpen, got %d", cb.State())
	}
}

func TestCircuitBreaker_HalfOpen(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	cb := newCircuitBreaker(1, 10*time.Millisecond, clk)

	// Trip the circuit.
	cb.RecordFailure()
	if !cb.IsOpen() {
		t.Fatal("expected circuit to be open")
	}

	// Let the cooldown expire.
	clk.advance(20 * time.Millisecond)

	// Should transition to half-open (IsOpen returns false to allow probe).
	if cb.IsOpen() {
		t.Fatal("expected circuit to transition to half-open after cooldown")
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("expected CircuitHalfOpen, got %d", cb.State())
	}

	// A success in half-open should close the circuit.
	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Fatalf("expected CircuitClosed after success in half-open, got %d", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenProbeFailure(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	cb := newCircuitBreaker(1, 10*time.Millisecond, clk)

	// Trip the circuit.
	cb.RecordFailure()

	// Let the cooldown expire.
	clk.advance(20 * time.Millisecond)

	// Transition to half-open.
	cb.IsOpen() // triggers transition

	// Probe fails — should go back to open.
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatalf("expected CircuitOpen after failed probe, got %d", cb.State())
	}
}

// TestCircuitBreaker_HalfOpenAdmitsOneProbe is the regression test for the
// half-open thundering herd: once the cooldown elapses, a burst of concurrent
// callers must see exactly one IsOpen()==false (the probe), and the rest must
// be refused until the probe reports.
func TestCircuitBreaker_HalfOpenAdmitsOneProbe(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	cb := newCircuitBreaker(1, time.Second, clk)
	cb.RecordFailure()
	clk.advance(2 * time.Second)

	const callers = 64
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
		start    = make(chan struct{})
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if !cb.IsOpen() {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != 1 {
		t.Fatalf("half-open admitted %d of %d concurrent callers, want exactly 1", admitted, callers)
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatalf("state = %d, want CircuitHalfOpen while the probe is outstanding", cb.State())
	}

	// The probe succeeds: the circuit closes and admits everyone again.
	cb.RecordSuccess()
	for i := 0; i < 3; i++ {
		if cb.IsOpen() {
			t.Fatal("expected closed circuit after successful probe")
		}
	}
}

// TestCircuitBreaker_UnreportedProbeIsReclaimed checks that a probe that never
// reports does not wedge the breaker half-open: after one cooldown the next
// caller becomes the probe.
func TestCircuitBreaker_UnreportedProbeIsReclaimed(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	cb := newCircuitBreaker(1, time.Second, clk)
	cb.RecordFailure()
	clk.advance(time.Second)

	if cb.IsOpen() {
		t.Fatal("expected the first caller after the cooldown to be the probe")
	}
	if !cb.IsOpen() {
		t.Fatal("expected a second caller to be refused while the probe is outstanding")
	}
	clk.advance(time.Second)
	if cb.IsOpen() {
		t.Fatal("expected the probe slot to be reclaimed after one cooldown")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

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
