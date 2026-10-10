// Package quotatest is a conformance suite for quota.Store implementations.
//
// RunStore checks the Store contract on its own; RunLimiter drives a
// quota.Limiter over the store through reserve, commit and cancel flows with
// an injected clock. A store that is not a quota.Recorder (one that reads the
// application's events, like quota.SQLStore) supplies Harness.Add to write
// those events, and the suite calls it before every Commit, which is the
// order the quota package asks applications to follow.
package quotatest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/llm-core/quota"
)

// Harness is one fresh, empty store under test.
type Harness struct {
	// Store is the store under test. It must start empty.
	Store quota.Store
	// Add makes e visible to Store under c the way the application would: an
	// inserted event row, an appended audit record. Required for stores that
	// are not a quota.Recorder; for a Recorder it may be nil, and the suite
	// records through the store.
	Add func(t testing.TB, c quota.Counter, e quota.Entry)
}

func (h Harness) add(t testing.TB, c quota.Counter, e quota.Entry) {
	t.Helper()
	if h.Add != nil {
		h.Add(t, c, e)
		return
	}
	rec, ok := h.Store.(quota.Recorder)
	if !ok {
		t.Fatalf("quotatest: %T is not a quota.Recorder and Harness.Add is nil", h.Store)
	}
	if err := rec.Record(context.Background(), c, e); err != nil {
		t.Fatalf("Record(%s, %+v): %v", c, e, err)
	}
}

// Epoch is the fixed time the suite's clocks start at.
var Epoch = time.Date(2026, time.March, 4, 10, 0, 0, 0, time.UTC)

// Clock is a manually advanced quota.Clock, safe for concurrent use.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now implements quota.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// RunStore runs the Store contract against stores made by newHarness, one per
// subtest.
func RunStore(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()
	c := quota.Counter{Key: "k", Unit: "tokens", Window: quota.Rolling(time.Hour).ID()}
	at := func(min int) time.Time { return Epoch.Add(time.Duration(min) * time.Minute) }

	t.Run("empty", func(t *testing.T) {
		h := newHarness(t)
		n, err := h.Store.Sum(ctx, c, at(0), at(60))
		if err != nil || n != 0 {
			t.Fatalf("Sum on empty store = %d, %v; want 0, nil", n, err)
		}
		es, err := h.Store.Entries(ctx, c, at(0), at(60))
		if err != nil || len(es) != 0 {
			t.Fatalf("Entries on empty store = %v, %v; want none, nil", es, err)
		}
	})

	t.Run("half-open range", func(t *testing.T) {
		h := newHarness(t)
		h.add(t, c, quota.Entry{At: at(-1), Amount: 1})    // before from
		h.add(t, c, quota.Entry{At: at(0), Amount: 10})    // at from: counted
		h.add(t, c, quota.Entry{At: at(30), Amount: 100})  // inside
		h.add(t, c, quota.Entry{At: at(60), Amount: 1000}) // at to: not counted
		n, err := h.Store.Sum(ctx, c, at(0), at(60))
		if err != nil || n != 110 {
			t.Fatalf("Sum [0,60) = %d, %v; want 110, nil", n, err)
		}
		n, err = h.Store.Sum(ctx, c, at(30), at(30))
		if err != nil || n != 0 {
			t.Fatalf("Sum over an empty range = %d, %v; want 0, nil", n, err)
		}
	})

	t.Run("counters are separate", func(t *testing.T) {
		h := newHarness(t)
		others := []quota.Counter{
			{Key: "other", Unit: c.Unit, Window: c.Window},
			{Key: c.Key, Unit: "requests", Window: c.Window},
		}
		if _, ok := h.Store.(quota.Recorder); ok && h.Add == nil {
			// A store that keeps usage per counter must also separate windows;
			// one that reads application events cannot tell windows apart.
			others = append(others, quota.Counter{Key: c.Key, Unit: c.Unit, Window: quota.Calendar(quota.Day, nil).ID()})
		}
		for _, o := range others {
			h.add(t, o, quota.Entry{At: at(5), Amount: 7})
		}
		h.add(t, c, quota.Entry{At: at(5), Amount: 3})
		n, err := h.Store.Sum(ctx, c, at(0), at(60))
		if err != nil || n != 3 {
			t.Fatalf("Sum(%s) = %d, %v; want 3, nil (other counters leaked in)", c, n, err)
		}
	})

	t.Run("entries oldest first and agree with sum", func(t *testing.T) {
		h := newHarness(t)
		for _, m := range []int{40, 10, 25, 10} {
			h.add(t, c, quota.Entry{At: at(m), Amount: int64(m)})
		}
		es, err := h.Store.Entries(ctx, c, at(0), at(60))
		if err != nil {
			t.Fatal(err)
		}
		if len(es) != 4 {
			t.Fatalf("Entries = %v; want 4 entries", es)
		}
		var total int64
		for i, e := range es {
			if i > 0 && e.At.Before(es[i-1].At) {
				t.Fatalf("Entries not oldest first: %v", es)
			}
			if !e.At.Equal(at(int(e.Amount))) {
				t.Fatalf("entry %+v: time and amount do not match what was added", e)
			}
			total += e.Amount
		}
		n, err := h.Store.Sum(ctx, c, at(0), at(60))
		if err != nil || n != total {
			t.Fatalf("Sum = %d, %v; want %d (the total of Entries)", n, err, total)
		}
	})
}

// RunLimiter drives a quota.Limiter over stores made by newHarness, with an
// injected clock, through the reserve/commit/cancel flows every store must
// support.
func RunLimiter(t *testing.T, newHarness func(t *testing.T) Harness) {
	ctx := context.Background()

	commit := func(t *testing.T, h Harness, r *quota.Reservation, actual int64) {
		t.Helper()
		if _, ok := h.Store.(quota.Recorder); !ok || h.Add != nil {
			h.add(t, r.Limit().Counter(), quota.Entry{At: r.At(), Amount: actual})
		}
		if err := r.Commit(ctx, actual); err != nil {
			t.Fatalf("Commit(%d): %v", actual, err)
		}
	}
	check := func(t *testing.T, l *quota.Limiter, lim quota.Limit, amount int64, want quota.Decision) {
		t.Helper()
		got, err := l.Check(ctx, lim, amount)
		if err != nil {
			t.Fatalf("Check(%d): %v", amount, err)
		}
		if got != want {
			t.Fatalf("Check(%d) = %+v; want %+v", amount, got, want)
		}
	}

	t.Run("rolling estimate then reconcile", func(t *testing.T) {
		h := newHarness(t)
		clk := NewClock(Epoch)
		l := quota.New(h.Store, quota.WithClock(clk))
		lim := quota.Limit{Key: "model", Unit: "input_tokens", Window: quota.Rolling(time.Minute), Max: 10}

		r1, d, err := l.Reserve(ctx, lim, 6)
		if err != nil || !d.Allowed || r1 == nil || d.Remaining != 10 {
			t.Fatalf("Reserve(6) = %v, %+v, %v; want a reservation with 10 remaining", r1, d, err)
		}
		clk.Advance(10 * time.Second)
		// The open estimate counts: 4 left.
		check(t, l, lim, 5, quota.Decision{Remaining: 4, RetryAfter: 50 * time.Second, Reason: quota.ReasonExhausted})
		check(t, l, lim, 11, quota.Decision{Remaining: 4, Reason: quota.ReasonTooLarge})
		// The response was smaller than the estimate.
		commit(t, h, r1, 3)
		check(t, l, lim, 7, quota.Decision{Allowed: true, Remaining: 7})

		r2, _, err := l.Reserve(ctx, lim, 7)
		if err != nil || r2 == nil {
			t.Fatalf("Reserve(7) = %v, %v", r2, err)
		}
		// A stream that ran over its estimate: the counter takes the actual.
		commit(t, h, r2, 9)
		check(t, l, lim, 1, quota.Decision{Remaining: 0, RetryAfter: 50 * time.Second, Reason: quota.ReasonExhausted})

		// r1's 3 leave the window 60s after r1 was made.
		clk.Advance(50 * time.Second)
		check(t, l, lim, 1, quota.Decision{Allowed: true, Remaining: 1})
		clk.Advance(10 * time.Second)
		check(t, l, lim, 10, quota.Decision{Allowed: true, Remaining: 10})
	})

	t.Run("cancel gives the estimate back", func(t *testing.T) {
		h := newHarness(t)
		clk := NewClock(Epoch)
		l := quota.New(h.Store, quota.WithClock(clk))
		lim := quota.Limit{Key: "caller", Unit: "requests", Window: quota.Rolling(time.Minute), Max: 1}
		r, _, err := l.Reserve(ctx, lim, 1)
		if err != nil || r == nil {
			t.Fatalf("Reserve = %v, %v", r, err)
		}
		if r2, d, _ := l.Reserve(ctx, lim, 1); r2 != nil || d.Allowed {
			t.Fatalf("second Reserve = %v, %+v; want denied", r2, d)
		}
		if err := r.Cancel(); err != nil {
			t.Fatal(err)
		}
		if err := r.Cancel(); err != quota.ErrReservationClosed {
			t.Fatalf("second Cancel = %v; want ErrReservationClosed", err)
		}
		check(t, l, lim, 1, quota.Decision{Allowed: true, Remaining: 1})
	})

	t.Run("calendar day counts from local midnight", func(t *testing.T) {
		h := newHarness(t)
		loc := time.FixedZone("UTC-6", -6*3600)
		now := time.Date(2026, time.March, 4, 9, 0, 0, 0, loc)
		clk := NewClock(now)
		l := quota.New(h.Store, quota.WithClock(clk))
		lim := quota.Limit{Key: "team", Unit: "usd_micros", Window: quota.Calendar(quota.Day, loc), Max: 8}
		c := lim.Counter()
		h.add(t, c, quota.Entry{At: time.Date(2026, time.March, 3, 23, 59, 0, 0, loc), Amount: 5}) // yesterday
		h.add(t, c, quota.Entry{At: time.Date(2026, time.March, 4, 0, 0, 0, 0, loc), Amount: 5})   // today
		check(t, l, lim, 3, quota.Decision{Allowed: true, Remaining: 3})
		check(t, l, lim, 4, quota.Decision{Remaining: 3, RetryAfter: 15 * time.Hour, Reason: quota.ReasonExhausted})
	})
}
