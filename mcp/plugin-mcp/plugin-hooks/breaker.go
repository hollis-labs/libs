package pluginhooks

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// BreakerState is scoped to one registry epoch, owner and load generation.
type BreakerState string

const (
	BreakerClosed   BreakerState = "closed"
	BreakerOpen     BreakerState = "open"
	BreakerHalfOpen BreakerState = "half_open"
	BreakerDisposed BreakerState = "disposed"
)

// BreakerConfig uses defaults of five consecutive failures and 30 seconds of
// cooldown when fields are zero. Negative values are invalid.
type BreakerConfig struct {
	FailureThreshold int
	Cooldown         time.Duration
}

// BreakerSnapshot is detached operator data. LastFailureClass contains only a
// classification, not handler error text. Reset preserves last-failure history.
type BreakerSnapshot struct {
	HostInstance        string       `json:"host_instance"`
	Owner               string       `json:"owner_id"`
	Generation          string       `json:"owner_generation"`
	State               BreakerState `json:"state"`
	ConsecutiveFailures int          `json:"consecutive_failures"`
	LastFailureClass    string       `json:"last_failure_class,omitempty"`
	LastFailureAt       time.Time    `json:"last_failure_at"`
	OpenUntil           time.Time    `json:"open_until"`
	ProbeActive         bool         `json:"probe_active"`
	Sequence            uint64       `json:"sequence"`
}

// BreakerEvent describes one serialized transition or manual reset. Sink calls
// may be delivered out of order; Snapshot.Sequence gives the state order.
type BreakerEvent struct {
	Previous BreakerState    `json:"previous"`
	Reason   string          `json:"reason"`
	At       time.Time       `json:"at"`
	Snapshot BreakerSnapshot `json:"snapshot"`
}
type breaker struct {
	snapshot BreakerSnapshot
	epoch    uint64
}
type breakerTicket struct {
	key   scopeKey
	epoch uint64
	probe bool
}

func (e *Engine) breakerLocked(key scopeKey) *breaker {
	b := e.breakers[key]
	if b == nil {
		b = &breaker{snapshot: BreakerSnapshot{HostInstance: e.registry.hostInstance, Owner: key.owner, Generation: key.generation, State: BreakerClosed}}
		e.breakers[key] = b
	}
	return b
}
func (e *Engine) transitionLocked(b *breaker, state BreakerState, reason string, now time.Time) *BreakerEvent {
	previous := b.snapshot.State
	b.snapshot.State = state
	b.snapshot.Sequence++
	return &BreakerEvent{Previous: previous, Reason: reason, At: now, Snapshot: b.snapshot}
}
func (e *Engine) admitBreaker(key scopeKey) (breakerTicket, error) {
	now := e.config.Clock.Now()
	e.breakerMu.Lock()
	b := e.breakerLocked(key)
	var event *BreakerEvent
	var err error
	switch b.snapshot.State {
	case BreakerDisposed:
		err = ErrUnavailable
	case BreakerOpen:
		if now.Before(b.snapshot.OpenUntil) {
			err = ErrUnavailable
		} else {
			b.epoch++
			b.snapshot.ProbeActive = true
			event = e.transitionLocked(b, BreakerHalfOpen, "cooldown_elapsed", now)
		}
	case BreakerHalfOpen:
		if b.snapshot.ProbeActive {
			err = ErrUnavailable
		} else {
			b.snapshot.ProbeActive = true
		}
	case BreakerClosed:
	default:
		err = ErrUnavailable
	}
	ticket := breakerTicket{key: key, epoch: b.epoch, probe: b.snapshot.State == BreakerHalfOpen && err == nil}
	e.breakerMu.Unlock()
	e.breakerEvent(event)
	return ticket, err
}
func excludedBreakerError(err error) bool {
	return errors.Is(err, ErrCancelled) || errors.Is(err, ErrApprovalRequired) || errors.Is(err, ErrDepthExceeded)
}
func (e *Engine) completeBreaker(ticket breakerTicket, err error, excluded bool, class string) BreakerSnapshot {
	now := e.config.Clock.Now()
	e.breakerMu.Lock()
	b := e.breakerLocked(ticket.key)
	var event *BreakerEvent
	if b.epoch == ticket.epoch && b.snapshot.State != BreakerDisposed {
		if ticket.probe {
			b.snapshot.ProbeActive = false
		}
		if !excluded && !excludedBreakerError(err) {
			if err == nil {
				b.snapshot.ConsecutiveFailures = 0
				if ticket.probe {
					b.epoch++
					b.snapshot.OpenUntil = time.Time{}
					event = e.transitionLocked(b, BreakerClosed, "probe_succeeded", now)
				}
			} else {
				b.snapshot.ConsecutiveFailures++
				b.snapshot.LastFailureClass = class
				b.snapshot.LastFailureAt = now
				if ticket.probe || b.snapshot.ConsecutiveFailures >= e.config.Breaker.FailureThreshold {
					b.epoch++
					b.snapshot.OpenUntil = now.Add(e.config.Breaker.Cooldown)
					event = e.transitionLocked(b, BreakerOpen, "handler_failure", now)
				}
			}
		}
	}
	snapshot := b.snapshot
	e.breakerMu.Unlock()
	e.breakerEvent(event)
	return snapshot
}

// Breaker returns the state of a known host-created owner generation.
func (e *Engine) Breaker(owner, generation string) (BreakerSnapshot, error) {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	key := scopeKey{owner, generation}
	if _, ok := e.registry.scopes[key]; !ok {
		return BreakerSnapshot{}, ErrUnauthorized
	}
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	return e.breakerLocked(key).snapshot, nil
}

// Breakers returns all known owner generations, including disposed ones.
func (e *Engine) Breakers() []BreakerSnapshot {
	e.registry.mu.Lock()
	e.breakerMu.Lock()
	out := make([]BreakerSnapshot, 0, len(e.registry.scopes))
	for key := range e.registry.scopes {
		out = append(out, e.breakerLocked(key).snapshot)
	}
	e.breakerMu.Unlock()
	e.registry.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Owner == out[j].Owner {
			return out[i].Generation < out[j].Generation
		}
		return out[i].Owner < out[j].Owner
	})
	return out
}

// ResetBreaker is a trusted-host operation. It starts a fresh accounting epoch,
// preserving failure history. It never releases permits or revives disposal.
func (e *Engine) ResetBreaker(owner, generation string) error {
	now := e.config.Clock.Now()
	e.registry.mu.Lock()
	key := scopeKey{owner, generation}
	state, ok := e.registry.scopes[key]
	if !ok {
		e.registry.mu.Unlock()
		return ErrUnauthorized
	}
	if state.disposed {
		e.registry.mu.Unlock()
		return ErrDisposed
	}
	e.breakerMu.Lock()
	b := e.breakerLocked(key)
	b.epoch++
	b.snapshot.ConsecutiveFailures = 0
	b.snapshot.ProbeActive = false
	b.snapshot.OpenUntil = time.Time{}
	event := e.transitionLocked(b, BreakerClosed, "manual_reset", now)
	e.breakerMu.Unlock()
	e.registry.mu.Unlock()
	e.breakerEvent(event)
	return nil
}

// disposeBreakerLocked requires the registry lock; callbacks run after unlocking.
func (e *Engine) disposeBreakerLocked(key scopeKey, now time.Time) *BreakerEvent {
	e.breakerMu.Lock()
	defer e.breakerMu.Unlock()
	b := e.breakerLocked(key)
	if b.snapshot.State == BreakerDisposed {
		return nil
	}
	b.epoch++
	b.snapshot.ProbeActive = false
	b.snapshot.OpenUntil = time.Time{}
	return e.transitionLocked(b, BreakerDisposed, "unload", now)
}

type observedCall struct {
	class    string
	once     sync.Once
	engine   *Engine
	dispatch *dispatch
	entry    *entry
	ticket   breakerTicket
	admitted bool
	started  bool
	beganAt  time.Time
	record   TraceRecord
}

func (e *Engine) observedCall(d *dispatch, entry *entry) *observedCall {
	reg := entry.registration
	spanCtx := context.WithValue(d.ctx, traceContextKey{}, TraceContext{TraceID: d.trace.TraceID, SpanID: d.trace.SpanID})
	trace, span, parent := e.traceIdentifiers(spanCtx)
	transport := "local"
	if entry.scope.remote {
		transport = "remote"
	}
	beganAt := e.config.Clock.Now()
	return &observedCall{engine: e, dispatch: d, entry: entry, beganAt: beganAt, record: TraceRecord{StartedAt: beganAt,
		Stage: "handler", TraceID: trace, SpanID: span, ParentSpanID: parent, InvocationID: d.id,
		Hook: reg.Hook, CatalogVersion: e.registry.catalogVersion, SchemaDigest: d.definition.SchemaDigest,
		HostInstance: e.registry.hostInstance, Owner: reg.Owner, Generation: reg.Generation, Registration: reg.Name,
		HandleID: reg.Handle.id, RegistrationSequence: reg.Sequence, Priority: reg.Options.Priority,
		Mode: d.definition.Mode, Depth: d.trace.Depth, Transport: transport,
		EffectiveTimeoutMS: milliseconds(reg.Options.Timeout), OnError: reg.Options.OnError,
	}}
}
func (call *observedCall) finish(err error) string {
	call.once.Do(func() {
		e := call.engine
		e.registry.mu.Lock()
		removed := call.entry.removed || call.entry.scope.disposed
		e.registry.mu.Unlock()
		excluded := !call.started || removed || call.dispatch.ctx.Err() != nil
		class := classify(err)
		// Canceled/unavailable sentinels returned by a running handler with live
		// contexts are ordinary handler failures, not synthetic admission/caller failures.
		if !excluded && (class == "caller_cancelled" || class == "unavailable") {
			class = "handler_error"
		}
		call.class = class
		var snapshot BreakerSnapshot
		if call.admitted {
			snapshot = e.completeBreaker(call.ticket, err, excluded, class)
		} else {
			e.breakerMu.Lock()
			snapshot = e.breakerLocked(call.entry.scope.key).snapshot
			e.breakerMu.Unlock()
		}
		record := call.record
		record.EndedAt = e.config.Clock.Now()
		record.DurationMS = elapsed(record.EndedAt, call.beganAt)
		record.Outcome = class
		record.ErrorClass = record.Outcome
		record.BreakerState = snapshot.State
		e.record(record)
	})
	return call.class
}
