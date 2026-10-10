package quota

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Limiter decides whether usage fits a Limit, holds estimates for requests in
// flight, and reconciles them with the actual amounts.
//
// The flow for one request is Reserve (check and hold an estimate), do the
// work, then Commit the actual amount or Cancel. For streaming responses the
// estimate is a guess and Commit's actual may be larger or smaller; the
// counter ends up with the actual. Check answers the same question as Reserve
// without holding anything.
//
// Open reservations live in the Limiter, in memory: they are what this process
// has promised and not yet recorded. Committed usage lives in the Store.
//
// A Limiter is safe for concurrent use. Decisions for all limits are
// serialized on one mutex, including the store reads they need, so that a
// check and the hold it leads to are atomic.
type Limiter struct {
	store Store
	clock Clock

	mu        sync.Mutex
	holds     map[Counter]map[*Reservation]struct{}
	snapshots map[Counter]ProviderSnapshot
}

// Option configures a Limiter.
type Option func(*Limiter)

// WithClock sets the clock the limiter reads; the default is the system clock.
func WithClock(c Clock) Option {
	return func(l *Limiter) {
		if c != nil {
			l.clock = c
		}
	}
}

// New returns a limiter over store.
func New(store Store, opts ...Option) *Limiter {
	l := &Limiter{
		store:     store,
		clock:     systemClock{},
		holds:     map[Counter]map[*Reservation]struct{}{},
		snapshots: map[Counter]ProviderSnapshot{},
	}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Check reports whether amount fits limit now. It holds nothing.
func (l *Limiter) Check(ctx context.Context, limit Limit, amount int64) (Decision, error) {
	if err := validate(limit, amount); err != nil {
		return Decision{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.decideLocked(ctx, limit, amount, l.clock.Now())
}

// Reserve checks amount against limit and, if it fits, holds it until the
// returned reservation is committed or cancelled. The reservation is nil when
// the decision is not Allowed. An allowed decision with an Unknown remaining
// (a provider window without a snapshot) still returns a reservation.
func (l *Limiter) Reserve(ctx context.Context, limit Limit, estimate int64) (*Reservation, Decision, error) {
	if err := validate(limit, estimate); err != nil {
		return nil, Decision{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	d, err := l.decideLocked(ctx, limit, estimate, now)
	if err != nil || !d.Allowed {
		return nil, d, err
	}
	r := &Reservation{l: l, limit: limit, counter: limit.Counter(), amount: estimate, at: now}
	set := l.holds[r.counter]
	if set == nil {
		set = map[*Reservation]struct{}{}
		l.holds[r.counter] = set
	}
	set[r] = struct{}{}
	return r, d, nil
}

// Observe records what the provider reported for a provider-window limit. A
// later snapshot replaces an earlier one. A zero ObservedAt is set to now.
func (l *Limiter) Observe(limit Limit, s ProviderSnapshot) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if limit.Window.Kind() != KindProvider {
		return ErrNotProviderWindow
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s.ObservedAt.IsZero() {
		s.ObservedAt = l.clock.Now()
	}
	l.snapshots[limit.Counter()] = s
	return nil
}

func validate(limit Limit, amount int64) error {
	if err := limit.Validate(); err != nil {
		return err
	}
	if amount < 0 {
		return ErrNegativeAmount
	}
	return nil
}

// heldLocked returns the open reservations on c made at or after from and
// before to, oldest first.
func (l *Limiter) heldLocked(c Counter, from, to time.Time) []Entry {
	var out []Entry
	for r := range l.holds[c] {
		if !r.at.Before(from) && r.at.Before(to) {
			out = append(out, Entry{At: r.at, Amount: r.amount})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func total(es []Entry) int64 {
	var n int64
	for _, e := range es {
		n += e.Amount
	}
	return n
}

func (l *Limiter) decideLocked(ctx context.Context, limit Limit, amount int64, now time.Time) (Decision, error) {
	c := limit.Counter()
	switch limit.Window.Kind() {
	case KindCalendar:
		start, end, _ := limit.Window.Bounds(now)
		used, err := l.store.Sum(ctx, c, start, end)
		if err != nil {
			return Decision{}, fmt.Errorf("quota: sum %s: %w", c, err)
		}
		used += total(l.heldLocked(c, start, end))
		return fixed(limit.Max, used, amount, end.Sub(now)), nil

	case KindRolling:
		start, end, _ := limit.Window.Bounds(now)
		committed, err := l.store.Entries(ctx, c, start, end)
		if err != nil {
			return Decision{}, fmt.Errorf("quota: entries %s: %w", c, err)
		}
		held := l.heldLocked(c, start, end)
		all := make([]Entry, 0, len(committed)+len(held))
		all = append(append(all, committed...), held...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].At.Before(all[j].At) })
		used := total(all)
		d := fixed(limit.Max, used, amount, 0)
		if d.Reason == ReasonExhausted {
			// Usage leaves the window oldest first; wait until enough has left.
			need := used + amount - limit.Max
			var freed int64
			for _, e := range all {
				freed += e.Amount
				if freed >= need {
					d.RetryAfter = max(e.At.Add(limit.Window.Duration()).Sub(now), time.Nanosecond)
					break
				}
			}
		}
		return d, nil

	default: // KindProvider
		s, ok := l.snapshots[c]
		if !ok {
			return Decision{Allowed: true, Remaining: Unknown, Reason: ReasonNoSnapshot}, nil
		}
		base, since, retry := s.Remaining, s.ObservedAt, time.Duration(0)
		if s.ResetsAt.IsZero() || now.Before(s.ResetsAt) {
			if !s.ResetsAt.IsZero() {
				retry = s.ResetsAt.Sub(now)
			}
		} else {
			// The provider's window has reset since the snapshot. Without the
			// full limit nothing is known until the next snapshot; with it, count
			// from the reset. The next reset time is not known.
			if s.Limit <= 0 {
				return Decision{Allowed: true, Remaining: Unknown, Reason: ReasonNoSnapshot}, nil
			}
			base, since = s.Limit, s.ResetsAt
		}
		end := now.Add(time.Nanosecond)
		used, err := l.store.Sum(ctx, c, since, end)
		if err != nil {
			return Decision{}, fmt.Errorf("quota: sum %s: %w", c, err)
		}
		used += total(l.heldLocked(c, since, end))
		d := fixed(base, used, amount, retry)
		if !d.Allowed {
			// Only the provider's full limit tells whether waiting can help.
			if s.Limit > 0 && amount > s.Limit {
				d.Reason, d.RetryAfter = ReasonTooLarge, 0
			} else {
				d.Reason, d.RetryAfter = ReasonExhausted, retry
			}
		}
		return d, nil
	}
}

// fixed decides amount against a budget of max with used already consumed.
// retry is the wait to report when the request does not fit now but would fit
// an empty window.
func fixed(maxAmount, used, amount int64, retry time.Duration) Decision {
	remaining := max(maxAmount-used, 0)
	switch {
	case amount <= remaining:
		return Decision{Allowed: true, Remaining: remaining}
	case amount > maxAmount:
		return Decision{Remaining: remaining, Reason: ReasonTooLarge}
	default:
		return Decision{Remaining: remaining, RetryAfter: retry, Reason: ReasonExhausted}
	}
}

// Reservation is an estimate held against a limit for a request in flight.
type Reservation struct {
	l       *Limiter
	limit   Limit
	counter Counter
	amount  int64
	at      time.Time
	closed  bool
}

// Limit returns the limit the reservation was made against.
func (r *Reservation) Limit() Limit { return r.limit }

// Amount returns the estimate held.
func (r *Reservation) Amount() int64 { return r.amount }

// At returns when the reservation was made. Commit records usage at this time,
// so a request is counted in the window it started in.
func (r *Reservation) At() time.Time { return r.at }

// Commit replaces the held estimate with the actual amount. When the store is a
// Recorder, the actual amount is recorded in it, dated At; when it is not, the
// application's own event is the record and must already be written. If
// recording fails the reservation stays open and the error is returned.
func (r *Reservation) Commit(ctx context.Context, actual int64) error {
	if actual < 0 {
		return ErrNegativeAmount
	}
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.closed {
		return ErrReservationClosed
	}
	if rec, ok := l.store.(Recorder); ok && actual > 0 {
		if err := rec.Record(ctx, r.counter, Entry{At: r.at, Amount: actual}); err != nil {
			return fmt.Errorf("quota: record %s: %w", r.counter, err)
		}
	}
	l.releaseLocked(r)
	return nil
}

// Cancel releases the held estimate without recording anything, for a request
// that did not run.
func (r *Reservation) Cancel() error {
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.closed {
		return ErrReservationClosed
	}
	l.releaseLocked(r)
	return nil
}

func (l *Limiter) releaseLocked(r *Reservation) {
	r.closed = true
	if set := l.holds[r.counter]; set != nil {
		delete(set, r)
		if len(set) == 0 {
			delete(l.holds, r.counter)
		}
	}
}
