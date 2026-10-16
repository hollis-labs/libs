package jobmw_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/libs/core/util/mw"
	"github.com/hollis-labs/libs/core/util/mw/jobmw"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
func (f *fakeClock) Advance(d time.Duration) { f.mu.Lock(); f.t = f.t.Add(d); f.mu.Unlock() }

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_800_000_000, 0)} }

func run(t *testing.T, h jobmw.Handler, j jobmw.Job, ms ...jobmw.Member) error {
	t.Helper()
	s, err := mw.Assemble(mw.Options{}, ms...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Wrap(h)(context.Background(), j)
	return err
}

func ok(context.Context, jobmw.Job) (struct{}, error) { return struct{}{}, nil }

func TestRateLimitReleasesWithoutSpendingAttempt(t *testing.T) {
	clk := newClock()
	l := &jobmw.MemLimiter{Clock: clk.Now}
	m := jobmw.RateLimited(l, jobmw.ByType, 2, time.Minute)
	j := jobmw.Job{ID: "1", Type: "send-email", Attempt: 1}

	for i := 0; i < 2; i++ {
		if err := run(t, ok, j, m); err != nil {
			t.Fatal(err)
		}
		clk.Advance(10 * time.Second)
	}
	err := run(t, ok, j, m)
	act, after := jobmw.Resolve(err)
	if act != jobmw.DoRelease || after != 40*time.Second {
		t.Fatalf("%v %v %v", err, act, after)
	}
	clk.Advance(41 * time.Second)
	if err := run(t, ok, j, m); err != nil {
		t.Fatalf("window should have slid: %v", err)
	}
	// different type is unaffected
	if err := run(t, ok, jobmw.Job{Type: "other"}, m); err != nil {
		t.Fatal(err)
	}
}

func TestWithoutOverlappingAndFencedUnlock(t *testing.T) {
	clk := newClock()
	l := &jobmw.MemLocker{Clock: clk.Now}
	m := jobmw.WithoutOverlapping(l, jobmw.ByKey, time.Minute, 5*time.Second)
	j := jobmw.Job{Key: "acct-9"}

	inner := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- run(t, func(context.Context, jobmw.Job) (struct{}, error) {
			close(inner)
			<-release
			return struct{}{}, nil
		}, j, m)
	}()
	<-inner
	act, after := jobmw.Resolve(run(t, ok, j, m))
	if act != jobmw.DoRelease || after != 5*time.Second {
		t.Fatalf("overlap: %v %v", act, after)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := run(t, ok, j, m); err != nil {
		t.Fatalf("lock not freed: %v", err)
	}

	// Fencing: holder A's lease expires, B takes over, A's late Unlock must not free B.
	ctx := context.Background()
	tokA, _, _ := l.TryLock(ctx, "k", time.Second)
	clk.Advance(2 * time.Second)
	tokB, gotB, _ := l.TryLock(ctx, "k", time.Minute)
	if !gotB || tokB == tokA {
		t.Fatal("B should take expired lock with a new token")
	}
	l.Unlock(ctx, "k", tokA)
	if _, again, _ := l.TryLock(ctx, "k", time.Minute); again {
		t.Fatal("stale unlock freed the new holder's lock")
	}
}

func TestOverlapLockedUnderRealConcurrency(t *testing.T) {
	l := &jobmw.MemLocker{}
	m := jobmw.WithoutOverlapping(l, jobmw.ByKey, time.Minute, time.Second)
	var running, maxRunning, released int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := run(t, func(context.Context, jobmw.Job) (struct{}, error) {
				n := atomic.AddInt32(&running, 1)
				for {
					old := atomic.LoadInt32(&maxRunning)
					if n <= old || atomic.CompareAndSwapInt32(&maxRunning, old, n) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&running, -1)
				return struct{}{}, nil
			}, jobmw.Job{Key: "k"}, m)
			if act, _ := jobmw.Resolve(err); act == jobmw.DoRelease {
				atomic.AddInt32(&released, 1)
			}
		}()
	}
	wg.Wait()
	if maxRunning != 1 {
		t.Fatalf("max concurrent = %d", maxRunning)
	}
}

func TestUniqueDropsDuplicates(t *testing.T) {
	l := &jobmw.MemLocker{}
	m := jobmw.UniqueByKey(l, time.Minute)
	inner, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- run(t, func(context.Context, jobmw.Job) (struct{}, error) {
			close(inner)
			<-release
			return struct{}{}, nil
		}, jobmw.Job{Key: "u"}, m)
	}()
	<-inner
	if act, _ := jobmw.Resolve(run(t, ok, jobmw.Job{Key: "u"}, m)); act != jobmw.DoDrop {
		t.Fatalf("want drop, got %v", act)
	}
	if err := run(t, ok, jobmw.Job{Key: ""}, m); err != nil {
		t.Fatal("keyless job must run")
	}
	close(release)
	<-done
}

func TestThrottleOnException(t *testing.T) {
	clk := newClock()
	c := &jobmw.MemCounter{Clock: clk.Now}
	m := jobmw.ThrottleOnException(c, jobmw.ByType, 3, time.Minute, 30*time.Second)
	boom := func(context.Context, jobmw.Job) (struct{}, error) { return struct{}{}, errors.New("upstream 503") }
	j := jobmw.Job{Type: "sync"}

	for i := 0; i < 3; i++ {
		if err := run(t, boom, j, m); err == nil || jobmw.IsRelease(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	called := false
	err := run(t, func(context.Context, jobmw.Job) (struct{}, error) { called = true; return struct{}{}, nil }, j, m)
	if act, after := jobmw.Resolve(err); act != jobmw.DoRelease || after != 30*time.Second || called {
		t.Fatalf("should throttle without running: %v %v called=%v", err, act, called)
	}
	clk.Advance(61 * time.Second)
	if err := run(t, ok, j, m); err != nil {
		t.Fatalf("window should clear: %v", err)
	}
}

func TestThrottleIgnoresPermanentErrors(t *testing.T) {
	c := &jobmw.MemCounter{}
	m := jobmw.ThrottleOnException(c, jobmw.ByType, 1, time.Minute, time.Second)
	bad := func(context.Context, jobmw.Job) (struct{}, error) {
		return struct{}{}, &jobmw.PermanentError{Err: errors.New("bad input")}
	}
	for i := 0; i < 5; i++ {
		if err := run(t, bad, jobmw.Job{Type: "x"}, m); jobmw.IsRelease(err) {
			t.Fatal("permanent errors must not trip the throttle")
		}
	}
}

func TestClassifyBackoffAndExhaustion(t *testing.T) {
	p := jobmw.RetryPolicy{
		Tries:   3,
		Backoff: []time.Duration{500 * time.Millisecond, 2 * time.Second},
		Classify: func(err error) jobmw.Class {
			if errors.Is(err, errBadInput) {
				return jobmw.Permanent
			}
			return jobmw.Transient
		},
	}
	m := jobmw.Classify(p)
	fail := func(e error) jobmw.Handler {
		return func(context.Context, jobmw.Job) (struct{}, error) { return struct{}{}, e }
	}

	act, after := jobmw.Resolve(run(t, fail(errors.New("503")), jobmw.Job{Attempt: 1}, m))
	if act != jobmw.DoRetry || after != 500*time.Millisecond {
		t.Fatalf("sub-second backoff must survive: %v %v", act, after)
	}
	_, after = jobmw.Resolve(run(t, fail(errors.New("503")), jobmw.Job{Attempt: 2}, m))
	if after != 2*time.Second {
		t.Fatalf("%v", after)
	}
	if act, _ := jobmw.Resolve(run(t, fail(errors.New("503")), jobmw.Job{Attempt: 3}, m)); act != jobmw.DoFail {
		t.Fatalf("exhausted should fail: %v", act)
	}
	if act, _ := jobmw.Resolve(run(t, fail(errBadInput), jobmw.Job{Attempt: 1}, m)); act != jobmw.DoFail {
		t.Fatalf("permanent class should fail at once: %v", act)
	}
	if act, _ := jobmw.Resolve(run(t, ok, jobmw.Job{Attempt: 1}, m)); act != jobmw.Complete {
		t.Fatal("success")
	}
}

var errBadInput = errors.New("bad input")

func TestBackoffLastValueRepeatsAndJitterBounded(t *testing.T) {
	p := jobmw.RetryPolicy{Backoff: []time.Duration{time.Second, 4 * time.Second}}
	if p.Delay(1) != time.Second || p.Delay(2) != 4*time.Second || p.Delay(9) != 4*time.Second {
		t.Fatal("repeat last")
	}
	p.Jitter = 0.25
	p.Rand = func() float64 { return 0 } // low end
	if p.Delay(2) != 3*time.Second {
		t.Fatalf("%v", p.Delay(2))
	}
	p.Rand = func() float64 { return 1 }
	if p.Delay(2) != 5*time.Second {
		t.Fatalf("%v", p.Delay(2))
	}
}

type brokenStore struct{}

func (brokenStore) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, 0, errors.New("pg down")
}

func TestStoreFailureFailsClosed(t *testing.T) {
	called := false
	err := run(t, func(context.Context, jobmw.Job) (struct{}, error) { called = true; return struct{}{}, nil },
		jobmw.Job{Type: "t"}, jobmw.RateLimited(brokenStore{}, jobmw.ByType, 1, time.Second))
	if mw.AsFault(err) == nil || called {
		t.Fatalf("closed member must not run the job unthrottled: err=%v called=%v", err, called)
	}
}

func TestFullChainAssemblesInPhaseOrder(t *testing.T) {
	clk := newClock()
	_, err := mw.Assemble(mw.Options{},
		mw.RecoverMember[jobmw.Job, struct{}](),
		mw.TimeoutMember[jobmw.Job, struct{}](time.Minute),
		jobmw.RateLimited(&jobmw.MemLimiter{Clock: clk.Now}, jobmw.ByType, 10, time.Minute),
		jobmw.WithoutOverlapping(&jobmw.MemLocker{Clock: clk.Now}, jobmw.ByKey, time.Minute, time.Second),
		jobmw.Classify(jobmw.RetryPolicy{Tries: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Classify outside rate-limit would be a phase violation.
	_, err = mw.Assemble(mw.Options{},
		jobmw.Classify(jobmw.RetryPolicy{}),
		jobmw.RateLimited(&jobmw.MemLimiter{}, jobmw.ByType, 1, time.Second),
	)
	if err == nil {
		t.Fatal("classify must be innermost")
	}
}
