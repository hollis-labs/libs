package llmcontracts

import (
	"time"

	"github.com/hollis-labs/substrate/llm-core/guard"
)

// CircuitState represents the circuit breaker state.
type CircuitState int

const (
	// CircuitClosed means normal operation — requests are allowed.
	CircuitClosed CircuitState = iota
	// CircuitOpen means the breaker has tripped — requests should be paused.
	CircuitOpen
	// CircuitHalfOpen means the breaker is probing — one request is allowed to test recovery.
	CircuitHalfOpen
)

// DefaultCooldown is the duration after which an open circuit transitions to half-open.
const DefaultCooldown = 30 * time.Second

// CircuitBreaker tracks consecutive failures and trips when a threshold is reached.
// It supports closed → open → half-open → closed state transitions.
// It is safe for concurrent use.
//
// It is implemented by guard.CircuitBreaker. Half-open admits exactly one
// probe: after the cooldown the first IsOpen returns false and every later
// IsOpen returns true until the probe reports with RecordSuccess or
// RecordFailure. A probe that never reports holds the slot for one cooldown,
// after which the next caller becomes the probe.
//
// Deprecated: use guard.CircuitBreaker, whose Admit returns an Admission that
// ignores results from calls admitted before a state change and can release
// an unused probe slot, or guard.Guard, which also applies cooldowns and
// error classification.
type CircuitBreaker struct {
	b *guard.CircuitBreaker
}

// NewCircuitBreaker creates a circuit breaker that trips after `threshold` consecutive failures.
func NewCircuitBreaker(threshold int) *CircuitBreaker {
	return newCircuitBreaker(threshold, DefaultCooldown, nil)
}

func newCircuitBreaker(threshold int, cooldown time.Duration, clk guard.Clock) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	return &CircuitBreaker{b: guard.NewCircuitBreaker(guard.BreakerConfig{
		Threshold: threshold,
		Cooldown:  cooldown,
		Clock:     clk,
	})}
}

// RecordFailure records a failure. Returns true if the circuit just tripped open.
func (cb *CircuitBreaker) RecordFailure() bool {
	return cb.b.RecordFailure()
}

// RecordSuccess records a success, resetting the consecutive failure counter.
// A success in any state closes the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.b.RecordSuccess()
}

// Reset reopens the circuit (moves from open back to closed) and resets counters.
func (cb *CircuitBreaker) Reset() {
	cb.b.Reset()
}

// IsOpen returns true if the circuit breaker is blocking requests.
// An open circuit transitions to half-open after the cooldown period and
// lets exactly one probe request through; it blocks every other request
// until that probe reports.
func (cb *CircuitBreaker) IsOpen() bool {
	return !cb.b.Allow()
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	switch cb.b.State() {
	case guard.CircuitOpen:
		return CircuitOpen
	case guard.CircuitHalfOpen:
		return CircuitHalfOpen
	default:
		return CircuitClosed
	}
}
