package quota_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/substrate/llm-core/quota"
	"github.com/hollis-labs/substrate/llm-core/quota/quotatest"
)

var ctx = context.Background()

func decide(t *testing.T, l *quota.Limiter, lim quota.Limit, amount int64, want quota.Decision) {
	t.Helper()
	got, err := l.Check(ctx, lim, amount)
	if err != nil {
		t.Fatalf("Check(%d): %v", amount, err)
	}
	if got != want {
		t.Fatalf("Check(%d) = %+v; want %+v", amount, got, want)
	}
}

func TestProviderWindow(t *testing.T) {
	clk := quotatest.NewClock(quotatest.Epoch)
	store := quota.NewMemoryStore()
	l := quota.New(store, quota.WithClock(clk))
	lim := quota.Limit{Key: "anthropic", Unit: "input_tokens", Window: quota.Provider("input-tokens-per-minute")}

	// No snapshot yet: allowed, but nothing is known.
	decide(t, l, lim, 1_000_000, quota.Decision{Allowed: true, Remaining: quota.Unknown, Reason: quota.ReasonNoSnapshot})

	// A request sent before the provider answered: its usage is in the figure.
	early, _, err := l.Reserve(ctx, lim, 500)
	if err != nil || early == nil {
		t.Fatalf("Reserve = %v, %v", early, err)
	}
	clk.Advance(time.Second)
	if err := l.Observe(lim, quota.ProviderSnapshot{
		Remaining: 1000, Limit: 4000, ResetsAt: quotatest.Epoch.Add(30 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := early.Commit(ctx, 600); err != nil {
		t.Fatal(err)
	}
	decide(t, l, lim, 1000, quota.Decision{Allowed: true, Remaining: 1000})

	// Usage after the snapshot is subtracted from what the provider reported.
	r, _, err := l.Reserve(ctx, lim, 700)
	if err != nil || r == nil {
		t.Fatalf("Reserve(700) = %v, %v", r, err)
	}
	decide(t, l, lim, 301, quota.Decision{Remaining: 300, RetryAfter: 29 * time.Second, Reason: quota.ReasonExhausted})
	decide(t, l, lim, 4001, quota.Decision{Remaining: 300, Reason: quota.ReasonTooLarge})
	if err := r.Commit(ctx, 800); err != nil {
		t.Fatal(err)
	}
	decide(t, l, lim, 200, quota.Decision{Allowed: true, Remaining: 200})

	// After the reset the full limit applies, counted from the reset.
	clk.Advance(30 * time.Second)
	decide(t, l, lim, 4000, quota.Decision{Allowed: true, Remaining: 4000})

	// A snapshot without the full limit says nothing once it has reset.
	if err := l.Observe(lim, quota.ProviderSnapshot{Remaining: 10, ResetsAt: clk.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	// Without the full limit, a large request is not "too large", only exhausted.
	decide(t, l, lim, 11, quota.Decision{Remaining: 10, RetryAfter: time.Second, Reason: quota.ReasonExhausted})
	clk.Advance(time.Second)
	decide(t, l, lim, 11, quota.Decision{Allowed: true, Remaining: quota.Unknown, Reason: quota.ReasonNoSnapshot})
}

func TestProviderWindowsNeverShareACounter(t *testing.T) {
	clk := quotatest.NewClock(quotatest.Epoch)
	l := quota.New(quota.NewMemoryStore(), quota.WithClock(clk))
	rolling := quota.Limit{Key: "k", Unit: "requests", Window: quota.Rolling(time.Minute), Max: 5}
	provider := quota.Limit{Key: "k", Unit: "requests", Window: quota.Provider("rpm")}
	if err := l.Observe(provider, quota.ProviderSnapshot{Remaining: 5, Limit: 5, ResetsAt: clk.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	r, _, err := l.Reserve(ctx, rolling, 5)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx, 5); err != nil {
		t.Fatal(err)
	}
	decide(t, l, provider, 5, quota.Decision{Allowed: true, Remaining: 5})
	decide(t, l, rolling, 1, quota.Decision{Remaining: 0, RetryAfter: time.Minute, Reason: quota.ReasonExhausted})
}

func TestObserveRefusesOtherWindows(t *testing.T) {
	l := quota.New(quota.NewMemoryStore())
	lim := quota.Limit{Key: "k", Unit: "u", Window: quota.Rolling(time.Minute), Max: 1}
	if err := l.Observe(lim, quota.ProviderSnapshot{}); !errors.Is(err, quota.ErrNotProviderWindow) {
		t.Fatalf("Observe on a rolling limit = %v; want ErrNotProviderWindow", err)
	}
}

func TestValidation(t *testing.T) {
	l := quota.New(quota.NewMemoryStore())
	good := quota.Limit{Key: "k", Unit: "u", Window: quota.Rolling(time.Minute), Max: 1}
	for _, lim := range []quota.Limit{
		{Unit: "u", Window: good.Window, Max: 1},
		{Key: "k", Window: good.Window, Max: 1},
		{Key: "k", Unit: "u", Max: 1},
		{Key: "k", Unit: "u", Window: good.Window, Max: -1},
	} {
		if _, err := l.Check(ctx, lim, 1); err == nil {
			t.Errorf("Check(%+v) = nil error", lim)
		}
	}
	if _, err := l.Check(ctx, good, -1); !errors.Is(err, quota.ErrNegativeAmount) {
		t.Errorf("Check(-1) = %v; want ErrNegativeAmount", err)
	}
	r, _, err := l.Reserve(ctx, good, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx, -1); !errors.Is(err, quota.ErrNegativeAmount) {
		t.Errorf("Commit(-1) = %v; want ErrNegativeAmount", err)
	}
	if err := r.Commit(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(ctx, 1); !errors.Is(err, quota.ErrReservationClosed) {
		t.Errorf("second Commit = %v; want ErrReservationClosed", err)
	}
	// A zero amount always fits, even an exhausted limit.
	decide(t, l, good, 0, quota.Decision{Allowed: true, Remaining: 0})
}

func TestZeroLimitDeniesEverythingAsTooLarge(t *testing.T) {
	l := quota.New(quota.NewMemoryStore())
	lim := quota.Limit{Key: "k", Unit: "u", Window: quota.Calendar(quota.Day, nil), Max: 0}
	decide(t, l, lim, 1, quota.Decision{Remaining: 0, Reason: quota.ReasonTooLarge})
}

type failingStore struct {
	quota.Store
	err error
}

func (f failingStore) Sum(context.Context, quota.Counter, time.Time, time.Time) (int64, error) {
	return 0, f.err
}

func (f failingStore) Entries(context.Context, quota.Counter, time.Time, time.Time) ([]quota.Entry, error) {
	return nil, f.err
}

func (f failingStore) Record(context.Context, quota.Counter, quota.Entry) error { return f.err }

func TestStoreErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	l := quota.New(failingStore{err: boom})
	for _, w := range []quota.Window{quota.Calendar(quota.Day, nil), quota.Rolling(time.Minute)} {
		lim := quota.Limit{Key: "k", Unit: "u", Window: w, Max: 1}
		if _, err := l.Check(ctx, lim, 1); !errors.Is(err, boom) {
			t.Errorf("%s: Check = %v; want boom", w, err)
		}
		if r, _, err := l.Reserve(ctx, lim, 1); r != nil || !errors.Is(err, boom) {
			t.Errorf("%s: Reserve = %v, %v; want nil, boom", w, r, err)
		}
	}

	// A failed record leaves the reservation open.
	mem := quota.NewMemoryStore()
	ok := quota.New(mem)
	lim := quota.Limit{Key: "k", Unit: "u", Window: quota.Provider("p")}
	r, _, err := ok.Reserve(ctx, lim, 1)
	if err != nil {
		t.Fatal(err)
	}
	bad := quota.New(failingStore{err: boom})
	r2, _, _ := bad.Reserve(ctx, lim, 1) // provider window, no snapshot: no store read
	if err := r2.Commit(ctx, 1); !errors.Is(err, boom) {
		t.Fatalf("Commit = %v; want boom", err)
	}
	if err := r2.Cancel(); err != nil {
		t.Fatalf("Cancel after a failed Commit = %v; want nil (still open)", err)
	}
	_ = r.Cancel()
}

// TestConcurrentReservationsNeverOverspend has many goroutines race for a small
// budget; the number admitted must equal the budget exactly.
func TestConcurrentReservationsNeverOverspend(t *testing.T) {
	clk := quotatest.NewClock(quotatest.Epoch)
	l := quota.New(quota.NewMemoryStore(), quota.WithClock(clk))
	lim := quota.Limit{Key: "k", Unit: "requests", Window: quota.Rolling(time.Minute), Max: 25}
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, d, err := l.Reserve(ctx, lim, 1)
			if err != nil {
				t.Error(err)
				return
			}
			if !d.Allowed {
				return
			}
			admitted.Add(1)
			if err := r.Commit(ctx, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := admitted.Load(); n != 25 {
		t.Fatalf("admitted %d; want exactly 25", n)
	}
	decide(t, l, lim, 1, quota.Decision{Remaining: 0, RetryAfter: time.Minute, Reason: quota.ReasonExhausted})
}

func TestSystemClockByDefault(t *testing.T) {
	l := quota.New(quota.NewMemoryStore())
	lim := quota.Limit{Key: "k", Unit: "u", Window: quota.Rolling(time.Hour), Max: 2}
	r, _, err := l.Reserve(ctx, lim, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(r.At()); d < 0 || d > time.Minute {
		t.Fatalf("reservation dated %s, %s from now", r.At(), d)
	}
	_ = r.Cancel()
}
