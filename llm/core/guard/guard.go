package guard

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Reason says why a Decision refused a call.
type Reason uint8

const (
	// ReasonNone is an admitted call.
	ReasonNone Reason = iota
	// ReasonCooldown means the key, its account or its resource is cooling
	// down; Decision.Class says after what.
	ReasonCooldown
	// ReasonQuota means the caller's QuotaCheck refused the call.
	ReasonQuota
	// ReasonCircuitOpen means the resource's breaker is open.
	ReasonCircuitOpen
	// ReasonProbeInFlight means the breaker is half-open and another call
	// holds the probe slot.
	ReasonProbeInFlight
)

// String returns the reason in lower case with hyphens.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonCooldown:
		return "cooldown"
	case ReasonQuota:
		return "quota"
	case ReasonCircuitOpen:
		return "circuit-open"
	case ReasonProbeInFlight:
		return "probe-in-flight"
	default:
		return "unknown"
	}
}

// Decision is the guard's answer for one key.
type Decision struct {
	Allowed bool
	// RetryAfter is how long to wait before asking again when refused; zero
	// when the guard cannot say (for example a QuotaCheck that gave none).
	RetryAfter time.Duration
	// Reason is the first check that refused the call.
	Reason Reason
	// Class is the failure class behind a ReasonCooldown refusal.
	Class Class
}

// QuotaCheck is the seam to a quota or budget source: it answers "how much
// remains" for key as a Decision. Only Allowed and RetryAfter are read. It
// must not have side effects that need undoing, because the guard may still
// refuse the call afterwards (for an open breaker).
type QuotaCheck func(Key) Decision

// Config configures a Guard. The zero value selects every default and no
// quota check.
type Config struct {
	// Breaker configures the breaker created for each resource and account.
	// Its Clock is replaced by Config.Clock when that is set.
	Breaker BreakerConfig
	// Cooldown configures the guard's Cooldown. Its Clock is replaced by
	// Config.Clock when that is set.
	Cooldown CooldownConfig
	// Quota, when set, is consulted after the cooldown and before the
	// breaker.
	Quota QuotaCheck
	// Clock is the time source for the breakers and the cooldown.
	Clock Clock
}

// Guard is the admission policy for calls to LLM resources. It combines, per
// Key, the Cooldown ("when may we retry"), an optional QuotaCheck ("how much
// remains") and a CircuitBreaker per resource and account ("is it healthy"),
// and records each call's outcome on them according to its Class. Safe for
// concurrent use.
type Guard struct {
	cfg      Config
	cooldown *Cooldown

	mu       sync.Mutex
	breakers map[Key]*CircuitBreaker
}

// New returns a Guard configured by cfg.
func New(cfg Config) *Guard {
	if cfg.Clock != nil {
		cfg.Breaker.Clock = cfg.Clock
		cfg.Cooldown.Clock = cfg.Clock
	}
	return &Guard{
		cfg:      cfg,
		cooldown: NewCooldown(cfg.Cooldown),
		breakers: make(map[Key]*CircuitBreaker),
	}
}

// Cooldown returns the guard's Cooldown, for recording resets learned out of
// band (Cooldown.Set) or clearing one by hand.
func (g *Guard) Cooldown() *Cooldown { return g.cooldown }

// breaker returns the breaker for key's resource and account. Breakers are
// per account because a resource reached with one credential can be healthy
// while another is not, and per resource rather than per model because
// connection and server failures are not model-specific.
func (g *Guard) breaker(key Key, create bool) *CircuitBreaker {
	bk := key.account()
	g.mu.Lock()
	defer g.mu.Unlock()
	b := g.breakers[bk]
	if b == nil && create {
		b = NewCircuitBreaker(g.cfg.Breaker)
		g.breakers[bk] = b
	}
	return b
}

// Ticket is one admitted call. Report its outcome with exactly one of Done or
// Release. The zero Ticket, from a refused Admit, is inert.
type Ticket struct {
	g   *Guard
	key Key
	adm Admission
}

// Probe reports whether this call is its breaker's half-open probe.
func (t Ticket) Probe() bool { return t.adm.Probe() }

// Done records the call's outcome and returns the Class it was recorded as
// (ClassUnknown for a nil err):
//
//   - nil: breaker success; ends the key's and account's cooldown.
//   - ClassRequest, ClassQuota, ClassAuth: the upstream answered, so breaker
//     success; quota and auth also set their cooldown.
//   - ClassTransient, ClassUnknown: breaker failure and a cooldown.
//   - ClassConnection: breaker failure, no cooldown.
//   - ClassCanceled: no verdict; the probe slot, if held, is freed.
func (t Ticket) Done(err error) Class {
	if t.g == nil {
		return ClassUnknown
	}
	if err == nil {
		t.adm.Success()
		t.g.cooldown.Success(t.key)
		return ClassUnknown
	}
	class, retry := Classify(err)
	switch class {
	case ClassCanceled:
		t.adm.Release()
	case ClassRequest:
		t.adm.Success()
	case ClassQuota, ClassAuth:
		t.adm.Success()
		t.g.cooldown.Record(t.key, class, retry)
	case ClassConnection:
		t.adm.Failure()
	default: // ClassTransient, ClassUnknown
		t.adm.Failure()
		t.g.cooldown.Record(t.key, class, retry)
	}
	return class
}

// Release gives the ticket back without a verdict, for a call that never ran.
func (t Ticket) Release() {
	if t.g != nil {
		t.adm.Release()
	}
}

// Check returns the Decision Admit would give for key, without side effects:
// it takes no probe slot. Use it to answer "when may we retry" without
// making a call.
func (g *Guard) Check(key Key) Decision {
	d := g.precheck(key)
	if !d.Allowed {
		return d
	}
	if b := g.breaker(key, false); b != nil {
		// An open breaker whose cooldown elapsed reports zero: the next
		// Admit becomes the probe.
		if wait := b.RetryAfter(); wait > 0 {
			return breakerRefusal(b, wait)
		}
	}
	return d
}

// Admit decides whether a call for key may proceed. It checks, in order, the
// cooldown, the QuotaCheck and the breaker; only the breaker check has a side
// effect (taking the half-open probe slot), so it runs last. When admitted,
// the caller must report the returned Ticket.
func (g *Guard) Admit(key Key) (Ticket, Decision) {
	d := g.precheck(key)
	if !d.Allowed {
		return Ticket{}, d
	}
	b := g.breaker(key, true)
	adm, ok := b.Admit()
	if !ok {
		return Ticket{}, breakerRefusal(b, b.RetryAfter())
	}
	return Ticket{g: g, key: key, adm: adm}, Decision{Allowed: true}
}

// precheck runs the side-effect-free checks: cooldown, then quota. When both
// refuse, the Reason is the cooldown's and RetryAfter the longer wait.
func (g *Guard) precheck(key Key) Decision {
	d := Decision{Allowed: true}
	if until, class, ok := g.cooldown.Until(key); ok {
		d = Decision{
			RetryAfter: max(until.Sub(g.cooldown.clk.Now()), 0),
			Reason:     ReasonCooldown,
			Class:      class,
		}
	}
	if g.cfg.Quota != nil {
		if q := g.cfg.Quota(key); !q.Allowed {
			if d.Allowed {
				return Decision{RetryAfter: max(q.RetryAfter, 0), Reason: ReasonQuota}
			}
			d.RetryAfter = max(d.RetryAfter, q.RetryAfter)
		}
	}
	return d
}

func breakerRefusal(b *CircuitBreaker, wait time.Duration) Decision {
	r := ReasonCircuitOpen
	if b.State() == CircuitHalfOpen {
		r = ReasonProbeInFlight
	}
	return Decision{RetryAfter: wait, Reason: r}
}

// ErrRefused is matched (errors.Is) by every error Do returns for a call the
// guard refused.
var ErrRefused = errors.New("guard: refused")

// RefusedError is returned by Do when the guard refused the call; fn was not
// called.
type RefusedError struct {
	Key      Key
	Decision Decision
}

// Error implements error.
func (e *RefusedError) Error() string {
	s := "guard: refused (" + e.Decision.Reason.String()
	if e.Decision.Reason == ReasonCooldown {
		s += ", " + e.Decision.Class.String()
	}
	if e.Decision.RetryAfter > 0 {
		s += ", retry after " + e.Decision.RetryAfter.String()
	}
	return s + ")"
}

// Is reports whether target is ErrRefused.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// RetryAfter returns the Decision's RetryAfter.
func (e *RefusedError) RetryAfter() time.Duration { return e.Decision.RetryAfter }

// GuardClass is ClassCanceled: a refused call never reached the upstream, so
// an outer guard records no verdict for it.
func (e *RefusedError) GuardClass() Class { return ClassCanceled }

// Do runs fn for key under the guard. A refused call returns a *RefusedError
// without calling fn. Otherwise fn's outcome is recorded as Ticket.Done
// describes and its error returned unchanged; a context.Canceled error while
// ctx is done, or a panic in fn, records no verdict.
func (g *Guard) Do(ctx context.Context, key Key, fn func(context.Context) error) error {
	t, d := g.Admit(key)
	if !d.Allowed {
		return &RefusedError{Key: key, Decision: d}
	}
	reported := false
	defer func() {
		if !reported {
			t.Release()
		}
	}()
	err := fn(ctx)
	reported = true
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Canceled) {
		t.Release()
		return err
	}
	t.Done(err)
	return err
}

// DoValue is Guard.Do for a function that returns a value. When the guard
// refuses the call the value is the zero T.
func DoValue[T any](ctx context.Context, g *Guard, key Key, fn func(context.Context) (T, error)) (T, error) {
	var out T
	err := g.Do(ctx, key, func(ctx context.Context) error {
		var ferr error
		out, ferr = fn(ctx)
		return ferr
	})
	return out, err
}

// State returns the circuit state of key's resource and account, without side
// effects; CircuitClosed when no call was ever admitted for it.
func (g *Guard) State(key Key) CircuitState {
	if b := g.breaker(key, false); b != nil {
		return b.State()
	}
	return CircuitClosed
}

// Reset drops the breaker of key's resource and account and clears key's own
// cooldown. Calls in flight report onto the discarded breaker.
func (g *Guard) Reset(key Key) {
	g.mu.Lock()
	delete(g.breakers, key.account())
	g.mu.Unlock()
	g.cooldown.Clear(key)
}
