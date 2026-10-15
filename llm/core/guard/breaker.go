package guard

import (
	"sync"
	"time"
)

// CircuitState is a CircuitBreaker's state.
type CircuitState int

const (
	// CircuitClosed is normal operation: calls are admitted.
	CircuitClosed CircuitState = iota
	// CircuitOpen means the breaker tripped: calls are refused until the
	// cooldown elapses.
	CircuitOpen
	// CircuitHalfOpen means the cooldown elapsed and one probe call has been,
	// or may be, admitted to test recovery.
	CircuitHalfOpen
)

// String returns "closed", "open" or "half-open".
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Defaults for BreakerConfig. They are starting points, not tuned against any
// particular provider.
const (
	DefaultThreshold = 5
	DefaultCooldown  = 30 * time.Second
)

// BreakerConfig configures a CircuitBreaker. The zero value selects every
// default.
type BreakerConfig struct {
	// Threshold is the number of consecutive failures that trips a closed
	// breaker. Below 1 selects DefaultThreshold.
	Threshold int
	// Cooldown is how long an open breaker refuses every call before it
	// admits a probe. At or below zero selects DefaultCooldown.
	Cooldown time.Duration
	// ProbeTimeout is how long an admitted half-open probe holds the probe
	// slot without reporting. After it, the slot is reclaimed and the next
	// caller becomes the probe; the late result of the old probe is ignored.
	// At or below zero selects Cooldown.
	ProbeTimeout time.Duration
	// Clock is the time source. Nil selects SystemClock.
	Clock Clock
}

// CircuitBreaker tracks consecutive failures of one resource, trips after
// Threshold of them, and half-opens after Cooldown. It is safe for concurrent
// use.
//
// Half-open admits exactly one probe. While the probe is outstanding every
// other caller is refused, so a burst of callers cannot reach a recovering
// (or still broken) upstream at once. The probe's success closes the circuit;
// its failure re-opens it and restarts the cooldown. A probe that never
// reports holds the slot for at most ProbeTimeout.
//
// Each admission carries an epoch that changes on every state transition and
// on Reset. A result reported through an Admission counts only if the epoch
// still matches, so a slow call admitted before a trip cannot close or
// re-open the circuit when it finally returns.
type CircuitBreaker struct {
	clk          Clock
	threshold    int
	cooldown     time.Duration
	probeTimeout time.Duration

	mu       sync.Mutex
	state    CircuitState
	fails    int
	openedAt time.Time
	probing  bool      // a half-open probe holds the slot
	probeAt  time.Time // when the current probe was admitted
	epoch    uint64    // bumped on every state transition, probe reclaim and Reset
}

// NewCircuitBreaker returns a closed breaker configured by cfg.
func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
	if cfg.Threshold < 1 {
		cfg.Threshold = DefaultThreshold
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultCooldown
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = cfg.Cooldown
	}
	return &CircuitBreaker{
		clk:          clockOr(cfg.Clock),
		threshold:    cfg.Threshold,
		cooldown:     cfg.Cooldown,
		probeTimeout: cfg.ProbeTimeout,
	}
}

type outcome uint8

const (
	outcomeSuccess outcome = iota
	outcomeFailure
	// outcomeAbandoned means the call never ran or the caller gave up. It
	// says nothing about upstream health and only frees a probe slot.
	outcomeAbandoned
)

// Admission is one admitted call. Report its result with exactly one of
// Success, Failure or Release. The zero Admission (from a refused Admit) is
// inert: its methods do nothing.
type Admission struct {
	cb    *CircuitBreaker
	epoch uint64
	probe bool
}

// Probe reports whether this admission is the half-open probe.
func (a Admission) Probe() bool { return a.probe }

// Success records that the admitted call reached a healthy upstream.
func (a Admission) Success() {
	if a.cb != nil {
		a.cb.done(a, outcomeSuccess)
	}
}

// Failure records that the admitted call found the upstream unhealthy. It
// returns true only when this failure tripped a closed circuit open.
func (a Admission) Failure() (tripped bool) {
	if a.cb == nil {
		return false
	}
	return a.cb.done(a, outcomeFailure)
}

// Release gives the admission back without a verdict, for a call that never
// ran or was abandoned by its caller. A released probe frees the slot for the
// next caller.
func (a Admission) Release() {
	if a.cb != nil {
		a.cb.done(a, outcomeAbandoned)
	}
}

// Admit reports whether a call may proceed and, if so, returns its Admission.
// It refuses while the circuit is open and the cooldown has not elapsed, and
// while a half-open probe holds the slot.
//
// Admit has a side effect: once the cooldown has elapsed the first caller
// moves the breaker to half-open and becomes the probe. Report every
// admission, or a probe holds the slot until ProbeTimeout.
func (cb *CircuitBreaker) Admit() (Admission, bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := cb.clk.Now()
	switch cb.state {
	case CircuitOpen:
		if now.Sub(cb.openedAt) < cb.cooldown {
			return Admission{}, false
		}
		cb.state = CircuitHalfOpen
		cb.epoch++
		return cb.takeProbeLocked(now), true
	case CircuitHalfOpen:
		if cb.probing {
			if now.Sub(cb.probeAt) < cb.probeTimeout {
				return Admission{}, false
			}
			// The probe never reported: reclaim the slot and ignore its
			// late result.
			cb.epoch++
		}
		return cb.takeProbeLocked(now), true
	default:
		return Admission{cb: cb, epoch: cb.epoch}, true
	}
}

func (cb *CircuitBreaker) takeProbeLocked(now time.Time) Admission {
	cb.probing = true
	cb.probeAt = now
	return Admission{cb: cb, epoch: cb.epoch, probe: true}
}

// Allow is Admit without the Admission, for callers that report through
// RecordSuccess, RecordFailure and Release. Those act on the breaker's
// current state rather than the state the call was admitted under, so prefer
// Admit where the call can outlive a state change.
func (cb *CircuitBreaker) Allow() bool {
	_, ok := cb.Admit()
	return ok
}

// RecordSuccess resets the failure count and closes the circuit from any
// state.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.doneLocked(cb.currentLocked(), outcomeSuccess)
}

// RecordFailure counts a failure against the current state. It returns true
// only on a fresh closed-to-open trip; a failed half-open probe re-opens the
// circuit and restarts the cooldown but returns false.
func (cb *CircuitBreaker) RecordFailure() (tripped bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.doneLocked(cb.currentLocked(), outcomeFailure)
}

// Release frees a half-open probe slot without a verdict. It does nothing in
// any other state.
func (cb *CircuitBreaker) Release() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.doneLocked(cb.currentLocked(), outcomeAbandoned)
}

// Reset forces the breaker closed and clears its counters. Results of calls
// admitted before the Reset are ignored when they arrive.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CircuitClosed
	cb.fails = 0
	cb.probing = false
	cb.epoch++
}

// State returns the current state without side effects. An open circuit whose
// cooldown has elapsed reads CircuitOpen until a call is admitted as the probe.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// RetryAfter returns how long until Admit could next admit a call: the rest
// of the cooldown while open, the rest of the probe's lease while a probe
// holds the slot, and zero otherwise. It has no side effects.
func (cb *CircuitBreaker) RetryAfter() time.Duration {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := cb.clk.Now()
	var d time.Duration
	switch {
	case cb.state == CircuitOpen:
		d = cb.cooldown - now.Sub(cb.openedAt)
	case cb.state == CircuitHalfOpen && cb.probing:
		d = cb.probeTimeout - now.Sub(cb.probeAt)
	}
	if d < 0 {
		return 0
	}
	return d
}

func (cb *CircuitBreaker) currentLocked() Admission {
	return Admission{cb: cb, epoch: cb.epoch, probe: cb.state == CircuitHalfOpen}
}

func (cb *CircuitBreaker) done(a Admission, o outcome) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.doneLocked(a, o)
}

func (cb *CircuitBreaker) doneLocked(a Admission, o outcome) (tripped bool) {
	if a.epoch != cb.epoch {
		return false // a transition happened since admission: stale result
	}
	switch o {
	case outcomeAbandoned:
		if a.probe && cb.state == CircuitHalfOpen {
			cb.probing = false
		}
	case outcomeSuccess:
		cb.fails = 0
		if cb.state != CircuitClosed {
			cb.state = CircuitClosed
			cb.probing = false
			cb.epoch++
		}
	case outcomeFailure:
		cb.fails++
		switch cb.state {
		case CircuitHalfOpen:
			cb.state = CircuitOpen
			cb.openedAt = cb.clk.Now()
			cb.probing = false
			cb.epoch++
		case CircuitOpen:
			// A late failure while open only counts; it does not restart
			// the cooldown.
		case CircuitClosed:
			if cb.fails >= cb.threshold {
				cb.state = CircuitOpen
				cb.openedAt = cb.clk.Now()
				cb.epoch++
				return true
			}
		}
	}
	return false
}
