package jobmw

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
)

// Job is the minimal view middleware needs.
type Job struct {
	ID      string
	Queue   string
	Type    string
	Key     string // dedupe / overlap key chosen at enqueue, may be empty
	Attempt int    // 1-based, counts only attempts that ran
	Payload []byte
}

// Handler runs one job.
type Handler = mw.Handler[Job, struct{}]

// Middleware wraps a job Handler.
type Middleware = mw.Middleware[Job, struct{}]

// Member is a job middleware with its descriptor and mode.
type Member = mw.Member[Job, struct{}]

// --- outcomes the worker acts on ---

// ReleaseError puts the job back after a delay without spending an attempt.
type ReleaseError struct {
	After  time.Duration
	Reason string
}

// Error describes the release.
func (e *ReleaseError) Error() string { return fmt.Sprintf("release in %s: %s", e.After, e.Reason) }

// DropError completes the job without running it (a duplicate, say). Not a failure.
type DropError struct{ Reason string }

// Error describes the drop.
func (e *DropError) Error() string { return "drop: " + e.Reason }

// PermanentError fails the job with no further retries.
type PermanentError struct{ Err error }

// Error describes the permanent failure.
func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }

// Unwrap returns the underlying error.
func (e *PermanentError) Unwrap() error { return e.Err }

// RetryError fails this attempt and retries after a delay, spending the attempt.
type RetryError struct {
	After time.Duration
	Err   error
}

// Error describes the retry.
func (e *RetryError) Error() string { return fmt.Sprintf("retry in %s: %v", e.After, e.Err) }

// Unwrap returns the underlying error.
func (e *RetryError) Unwrap() error { return e.Err }

// Action is what the worker does with a job after its handler returns.
type Action string

const (
	// Complete means the job succeeded.
	Complete Action = "complete"
	// DoRelease puts the job back after a delay without spending an attempt.
	DoRelease Action = "release"
	// DoDrop completes the job without running it.
	DoDrop Action = "drop"
	// DoRetry fails this attempt and retries after a delay.
	DoRetry Action = "retry"
	// DoFail fails the job with no further retries.
	DoFail Action = "fail"
)

// Resolve maps a handler result to the worker's action. Untyped errors are
// treated as retry-with-no-delay-info; put Classify in the chain to avoid that.
func Resolve(err error) (Action, time.Duration) {
	var (
		rel  *ReleaseError
		drop *DropError
		perm *PermanentError
		rt   *RetryError
	)
	switch {
	case err == nil:
		return Complete, 0
	case errors.As(err, &drop):
		return DoDrop, 0
	case errors.As(err, &rel):
		return DoRelease, rel.After
	case errors.As(err, &perm):
		return DoFail, 0
	case errors.As(err, &rt):
		return DoRetry, rt.After
	}
	return DoRetry, 0
}

// --- clock and stores ---

// Clock supplies the time to the in-memory stores; nil means time.Now.
type Clock func() time.Time

func (c Clock) now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// Limiter admits up to n events per window per key.
type Limiter interface {
	// Allow reports whether the event fits; when not, how long until it would.
	Allow(ctx context.Context, key string, n int, window time.Duration) (ok bool, retryAfter time.Duration, err error)
}

// Locker is a lease with a fencing token: Unlock with a stale token is a no-op,
// so a slow holder whose lease expired cannot release the new holder's lock.
type Locker interface {
	TryLock(ctx context.Context, key string, ttl time.Duration) (token uint64, ok bool, err error)
	Unlock(ctx context.Context, key string, token uint64) error
}

// Counter counts events in a sliding window.
type Counter interface {
	Add(ctx context.Context, key string, window time.Duration) (count int, err error)
	Count(ctx context.Context, key string, window time.Duration) (count int, oldestAge time.Duration, err error)
}

// --- in-memory implementations ---

// MemLimiter is an in-memory sliding-window Limiter.
type MemLimiter struct {
	Clock Clock
	mu    sync.Mutex
	hits  map[string][]time.Time
}

// Allow admits the event if fewer than n fit in the window.
func (m *MemLimiter) Allow(_ context.Context, key string, n int, window time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hits == nil {
		m.hits = map[string][]time.Time{}
	}
	now := m.Clock.now()
	h := prune(m.hits[key], now, window)
	if len(h) >= n {
		m.hits[key] = h
		return false, h[0].Add(window).Sub(now), nil
	}
	m.hits[key] = append(h, now)
	return true, 0, nil
}

func prune(h []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(h) && !h[i].After(now.Add(-window)) {
		i++
	}
	return h[i:]
}

// MemLocker is an in-memory Locker with fencing tokens.
type MemLocker struct {
	Clock Clock
	mu    sync.Mutex
	next  uint64
	held  map[string]struct {
		token uint64
		until time.Time
	}
}

// TryLock takes the lock unless a live lease holds it.
func (m *MemLocker) TryLock(_ context.Context, key string, ttl time.Duration) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.held == nil {
		m.held = map[string]struct {
			token uint64
			until time.Time
		}{}
	}
	now := m.Clock.now()
	if h, ok := m.held[key]; ok && now.Before(h.until) {
		return 0, false, nil
	}
	m.next++
	m.held[key] = struct {
		token uint64
		until time.Time
	}{m.next, now.Add(ttl)}
	return m.next, true, nil
}

// Unlock frees the lock only if token is the current holder's.
func (m *MemLocker) Unlock(_ context.Context, key string, token uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.held[key]; ok && h.token == token {
		delete(m.held, key)
	}
	return nil
}

// MemCounter is an in-memory sliding-window Counter.
type MemCounter struct {
	Clock Clock
	mu    sync.Mutex
	hits  map[string][]time.Time
}

// Add records one event and returns the count in the window.
func (m *MemCounter) Add(_ context.Context, key string, window time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hits == nil {
		m.hits = map[string][]time.Time{}
	}
	now := m.Clock.now()
	h := append(prune(m.hits[key], now, window), now)
	m.hits[key] = h
	return len(h), nil
}

// Count returns the events in the window and the age of the oldest.
func (m *MemCounter) Count(_ context.Context, key string, window time.Duration) (int, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Clock.now()
	h := prune(m.hits[key], now, window)
	if len(h) == 0 {
		delete(m.hits, key)
		return 0, 0, nil
	}
	m.hits[key] = h
	return len(h), now.Sub(h[0]), nil
}

// --- members ---

// KeyFunc derives a middleware key from a job. Empty means "do not apply".
type KeyFunc func(Job) string

// ByType keys on the job type; ByKey on the enqueue-time key.
func ByType(j Job) string { return j.Type }

// ByKey keys on the enqueue-time key.
func ByKey(j Job) string { return j.Key }

// RateLimited admits n runs per window per key; excess jobs are released with
// the time until a slot frees, not failed and not charged an attempt.
// Store errors are Faults (closed: the job is not run unthrottled).
func RateLimited(l Limiter, key KeyFunc, n int, window time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "rate-limited", Phase: mw.PhaseRateLimit, Failure: mw.Closed},
		MW: func(next Handler) Handler {
			return func(ctx context.Context, j Job) (struct{}, error) {
				k := key(j)
				if k == "" {
					return next(ctx, j)
				}
				ok, after, err := l.Allow(ctx, "rl:"+k, n, window)
				if err != nil {
					return struct{}{}, mw.NewFault(err)
				}
				if !ok {
					return struct{}{}, &ReleaseError{After: after, Reason: "rate limited: " + k}
				}
				return next(ctx, j)
			}
		},
	}
}

// WithoutOverlapping lets one job per key run at a time. A job that finds the
// key held is released after releaseAfter. The lock lease is ttl; set it above
// the longest expected run, since expiry (not the holder) frees a wedged lock.
// Unlock is fenced, so a holder that outlived its lease cannot free the new one.
func WithoutOverlapping(l Locker, key KeyFunc, ttl, releaseAfter time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "without-overlapping", Phase: mw.PhaseRateLimit, Failure: mw.Closed},
		MW: func(next Handler) Handler {
			return func(ctx context.Context, j Job) (struct{}, error) {
				k := key(j)
				if k == "" {
					return next(ctx, j)
				}
				tok, ok, err := l.TryLock(ctx, "ov:"+k, ttl)
				if err != nil {
					return struct{}{}, mw.NewFault(err)
				}
				if !ok {
					return struct{}{}, &ReleaseError{After: releaseAfter, Reason: "overlap: " + k}
				}
				// Unlock on a context that survives cancellation of the job's own.
				defer l.Unlock(context.WithoutCancel(ctx), "ov:"+k, tok)
				return next(ctx, j)
			}
		},
	}
}

// UniqueByKey drops a job whose key is already being processed. Dropping
// completes the job without spending an attempt.
// Enqueue-time uniqueness (refuse to insert a duplicate) belongs to the queue
// service; this is the execution-time backstop.
func UniqueByKey(l Locker, ttl time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "unique-by-key", Phase: mw.PhaseRateLimit, Failure: mw.Closed},
		MW: func(next Handler) Handler {
			return func(ctx context.Context, j Job) (struct{}, error) {
				if j.Key == "" {
					return next(ctx, j)
				}
				tok, ok, err := l.TryLock(ctx, "uq:"+j.Key, ttl)
				if err != nil {
					return struct{}{}, mw.NewFault(err)
				}
				if !ok {
					return struct{}{}, &DropError{Reason: "duplicate of running job: " + j.Key}
				}
				defer l.Unlock(context.WithoutCancel(ctx), "uq:"+j.Key, tok)
				return next(ctx, j)
			}
		},
	}
}

// ThrottleOnException stops hammering a failing dependency: once a key has seen
// max errors within window, jobs for it are released for cooldown without
// running. Errors that are PermanentError or DropError do not count (they say
// nothing about the dependency's health).
func ThrottleOnException(c Counter, key KeyFunc, max int, window, cooldown time.Duration) Member {
	return Member{
		Desc: mw.Descriptor{Name: "throttle-on-exception", Phase: mw.PhaseRateLimit, Failure: mw.Closed},
		MW: func(next Handler) Handler {
			return func(ctx context.Context, j Job) (struct{}, error) {
				k := key(j)
				if k == "" {
					return next(ctx, j)
				}
				n, _, err := c.Count(ctx, "te:"+k, window)
				if err != nil {
					return struct{}{}, mw.NewFault(err)
				}
				if n >= max {
					return struct{}{}, &ReleaseError{After: cooldown, Reason: fmt.Sprintf("throttled: %d errors in %s: %s", n, window, k)}
				}
				out, herr := next(ctx, j)
				if herr != nil && counts(herr) {
					if _, err := c.Add(ctx, "te:"+k, window); err != nil {
						return out, herr // keep the job's own error; losing a count is not worth masking it
					}
				}
				return out, herr
			}
		},
	}
}

func counts(err error) bool {
	var p *PermanentError
	var d *DropError
	var r *ReleaseError
	return !errors.As(err, &p) && !errors.As(err, &d) && !errors.As(err, &r)
}

// --- retry / backoff classification ---

// Class says whether an error is worth another attempt.
type Class int

const (
	Transient Class = iota // retry (default for unclassified errors)
	Permanent              // never retry: bad input, 4xx other than 408/429
)

// RetryPolicy is the attempt budget and backoff used by Classify.
type RetryPolicy struct {
	Tries int // total attempts allowed; 0 means 1
	// Backoff[i] is the delay after attempt i+1; the last value repeats. Sub-second
	// values are honored exactly, never truncated to whole seconds.
	Backoff []time.Duration
	// Jitter is the fraction of the delay randomized either way, 0 to 1.
	Jitter   float64
	Classify func(error) Class
	Rand     func() float64 // for tests; defaults to math/rand/v2
}

// Delay returns the backoff after the given attempt (1-based).
func (p RetryPolicy) Delay(attempt int) time.Duration {
	if len(p.Backoff) == 0 || attempt < 1 {
		return 0
	}
	i := attempt - 1
	if i >= len(p.Backoff) {
		i = len(p.Backoff) - 1
	}
	d := p.Backoff[i]
	if p.Jitter > 0 {
		r := rand.Float64
		if p.Rand != nil {
			r = p.Rand
		}
		d = time.Duration(float64(d) * (1 + p.Jitter*(2*r()-1)))
	}
	return d
}

// Classify types handler errors for the worker. Put it innermost of the job
// members so rate-limit/overlap releases (already typed) pass through untouched
// while raw handler errors become RetryError or PermanentError. It owns the
// attempt budget; the queue stores only the attempt count.
func Classify(p RetryPolicy) Member {
	tries := p.Tries
	if tries < 1 {
		tries = 1
	}
	return Member{
		Desc: mw.Descriptor{Name: "classify", Phase: mw.PhaseOutcome, Failure: mw.Closed},
		MW: func(next Handler) Handler {
			return func(ctx context.Context, j Job) (struct{}, error) {
				out, err := next(ctx, j)
				if err == nil || typed(err) {
					return out, err
				}
				switch {
				case p.Classify != nil && p.Classify(err) == Permanent:
					return out, &PermanentError{Err: err}
				case j.Attempt >= tries:
					return out, &PermanentError{Err: fmt.Errorf("attempts exhausted (%d): %w", tries, err)}
				}
				return out, &RetryError{After: p.Delay(j.Attempt), Err: err}
			}
		},
	}
}

func typed(err error) bool {
	var (
		rel  *ReleaseError
		drop *DropError
		perm *PermanentError
		rt   *RetryError
	)
	return errors.As(err, &rel) || errors.As(err, &drop) || errors.As(err, &perm) || errors.As(err, &rt)
}

// IsRelease reports whether err asks for the job to be released.
func IsRelease(err error) bool {
	var r *ReleaseError
	return errors.As(err, &r)
}
