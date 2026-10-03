package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock               { return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func breakerState(t *testing.T, e *Engine) BreakerSnapshot {
	t.Helper()
	snapshot, err := e.Breaker("fixture-owner", "one")
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func emit(t *testing.T, e *Engine, d Definition) (DispatchResult, error) {
	t.Helper()
	return e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
}
func TestBreakerDefaultsAndSuccessReset(t *testing.T) {
	clock := newFakeClock()
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock})
	var fail atomic.Bool
	fail.Store(true)
	action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error {
		if fail.Load() {
			return errors.New("private-error")
		}
		return nil
	})
	for range 4 {
		_, err := emit(t, e, d)
		if err == nil {
			t.Fatal("failure lost")
		}
	}
	if breakerState(t, e).ConsecutiveFailures != 4 || breakerState(t, e).State != BreakerClosed {
		t.Fatal(breakerState(t, e))
	}
	fail.Store(false)
	if _, err := emit(t, e, d); err != nil {
		t.Fatal(err)
	}
	if breakerState(t, e).ConsecutiveFailures != 0 {
		t.Fatal("success did not reset")
	}
	fail.Store(true)
	for range 5 {
		_, err := emit(t, e, d)
		if err == nil {
			t.Fatal("failure lost")
		}
	}
	snapshot := breakerState(t, e)
	if snapshot.State != BreakerOpen || snapshot.ConsecutiveFailures != 5 || snapshot.LastFailureClass != "handler_error" || !snapshot.OpenUntil.Equal(clock.Now().Add(30*time.Second)) {
		t.Fatal(snapshot)
	}
	if _, err := emit(t, e, d); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	clock.Advance(30*time.Second - time.Nanosecond)
	if _, err := emit(t, e, d); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	clock.Advance(time.Nanosecond)
	fail.Store(false)
	if _, err := emit(t, e, d); err != nil {
		t.Fatal(err)
	}
	if breakerState(t, e).State != BreakerClosed {
		t.Fatal("probe did not close")
	}
}
func TestBreakerCountedAndExcludedErrors(t *testing.T) {
	tests := []struct {
		name    string
		failure error
		panics  bool
		counted bool
	}{
		{"ordinary", errors.New("private message"), false, true},
		{"transport", fmt.Errorf("%w: private transport detail", ErrTransport), false, true},
		{"panic", nil, true, true},
		{"timeout", context.DeadlineExceeded, false, true},
		{"veto", ErrCancelled, false, false},
		{"approval", ErrApprovalRequired, false, false},
		{"depth", ErrDepthExceeded, false, false},
		{"handler_cancelled", context.Canceled, false, true},
		{"handler_unavailable", ErrUnavailable, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := definition("event.ready", Action)
			d.Mode = Bail
			e, s := engineFixture(t, d, ExecutionConfig{Clock: newFakeClock(), Breaker: BreakerConfig{FailureThreshold: 1}})
			action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error {
				if test.panics {
					panic("private panic")
				}
				return test.failure
			})
			_, _ = emit(t, e, d)
			snapshot := breakerState(t, e)
			if (snapshot.State == BreakerOpen) != test.counted {
				t.Fatal(snapshot)
			}
		})
	}
}
func TestBreakerInvalidOutputAndSkipPolicies(t *testing.T) {
	clock := newFakeClock()
	d := definition("value.change", Filter)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Breaker: BreakerConfig{FailureThreshold: 1}})
	var calls atomic.Int32
	filter(t, s, d, "bad", Options{OnError: Open}, func(context.Context, Invocation) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`{"visible":2,"hidden":"invented"}`), nil
	})
	input := json.RawMessage(`{"visible":1,"hidden":"keep"}`)
	result, err := e.ApplyFilters(context.Background(), d.Name, input, nil)
	if err != nil || result.Status != CompletedWithOpenErrors || string(result.Value) != string(input) || breakerState(t, e).LastFailureClass != "invalid_output" {
		t.Fatal(result, err, breakerState(t, e))
	}
	result, err = e.ApplyFilters(context.Background(), d.Name, input, nil)
	if err != nil || result.Status != CompletedWithOpenErrors || string(result.Value) != string(input) || calls.Load() != 1 || !errors.Is(result.Outcomes[0].Error, ErrUnavailable) {
		t.Fatal(result, err, calls.Load())
	}
	// A closed gate under the same owner's breaker cannot fabricate permission.
	gate := definition("operation.before", Action)
	gate.Mode = Bail
	r, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{d, gate}})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := r.NewScope(ScopeConfig{Owner: "fixture-owner", Generation: "one", Hooks: []string{d.Name, gate.Name}})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(r, ExecutionConfig{Clock: clock, Breaker: BreakerConfig{FailureThreshold: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })
	filter(t, scope, d, "fail", Options{OnError: Open}, func(context.Context, Invocation) (json.RawMessage, error) { return nil, errors.New("failure") })
	action(t, scope, gate, "authorize", Options{}, func(context.Context, Invocation) error { t.Error("open breaker invoked gate"); return nil })
	_, _ = engine.ApplyFilters(context.Background(), d.Name, input, nil)
	result, err = emit(t, engine, gate)
	if !errors.Is(err, ErrUnavailable) || result.Status != FailedClosed {
		t.Fatal(result, err)
	}
}
func TestConcurrentBreakerHalfOpenProbe(t *testing.T) {
	clock := newFakeClock()
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Breaker: BreakerConfig{FailureThreshold: 1}})
	var ready atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	action(t, s, d, "probe", Options{}, func(context.Context, Invocation) error {
		if !ready.Load() {
			return errors.New("failure")
		}
		close(entered)
		<-release
		return nil
	})
	_, _ = emit(t, e, d)
	clock.Advance(30 * time.Second)
	ready.Store(true)
	done := make(chan error, 1)
	go func() { _, err := emit(t, e, d); done <- err }()
	<-entered
	if snapshot := breakerState(t, e); snapshot.State != BreakerHalfOpen || !snapshot.ProbeActive {
		t.Fatal(snapshot)
	}
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if _, err := emit(t, e, d); !errors.Is(err, ErrUnavailable) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snapshot := breakerState(t, e); snapshot.State != BreakerClosed || snapshot.ProbeActive {
		t.Fatal(snapshot)
	}
}
func TestBreakerProbeFailureAndExcludedRetry(t *testing.T) {
	clock := newFakeClock()
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, Breaker: BreakerConfig{FailureThreshold: 1}})
	var kind atomic.Int32
	action(t, s, d, "handler", Options{}, func(context.Context, Invocation) error {
		switch kind.Load() {
		case 1:
			return ErrApprovalRequired
		case 2:
			return nil
		default:
			return errors.New("failure")
		}
	})
	_, _ = emit(t, e, d)
	clock.Advance(30 * time.Second)
	_, _ = emit(t, e, d)
	if snapshot := breakerState(t, e); snapshot.State != BreakerOpen || !snapshot.OpenUntil.Equal(clock.Now().Add(30*time.Second)) {
		t.Fatal(snapshot)
	}
	clock.Advance(30 * time.Second)
	kind.Store(1)
	_, _ = emit(t, e, d)
	if snapshot := breakerState(t, e); snapshot.State != BreakerHalfOpen || snapshot.ProbeActive || snapshot.ConsecutiveFailures != 2 {
		t.Fatal(snapshot)
	}
	kind.Store(2)
	if _, err := emit(t, e, d); err != nil {
		t.Fatal(err)
	}
	if breakerState(t, e).State != BreakerClosed {
		t.Fatal("excluded probe prevented retry")
	}
}
func TestBreakerGenerationIsolationAndDisposal(t *testing.T) {
	d := definition("event.ready", Action)
	e, old := engineFixture(t, d, ExecutionConfig{Clock: newFakeClock(), Breaker: BreakerConfig{FailureThreshold: 1}})
	action(t, old, d, "handler", Options{}, func(context.Context, Invocation) error { return errors.New("failure") })
	_, _ = emit(t, e, d)
	if err := old.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := breakerState(t, e); snapshot.State != BreakerDisposed {
		t.Fatal(snapshot)
	}
	if err := e.ResetBreaker("fixture-owner", "one"); !errors.Is(err, ErrDisposed) {
		t.Fatal(err)
	}
	if err := e.ResetBreaker("missing", "one"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	fresh, err := e.registry.NewScope(ScopeConfig{Owner: "fixture-owner", Generation: "two", Hooks: []string{d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	action(t, fresh, d, "handler", Options{}, func(context.Context, Invocation) error { return nil })
	if _, emitErr := emit(t, e, d); emitErr != nil {
		t.Fatal(emitErr)
	}
	snapshot, err := e.Breaker("fixture-owner", "two")
	if err != nil || snapshot.State != BreakerClosed || snapshot.HostInstance != e.registry.HostInstance() {
		t.Fatal(snapshot, err)
	}
	if len(e.Breakers()) != 2 {
		t.Fatal(e.Breakers())
	}
	// A scope disposed before engine construction stays disposed in operator data.
	r, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := r.NewScope(ScopeConfig{Owner: "old", Generation: "one"})
	if err != nil {
		t.Fatal(err)
	}
	_ = scope.Dispose(context.Background())
	engine, err := NewEngine(r, ExecutionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })
	snapshot, err = engine.Breaker("old", "one")
	if err != nil || snapshot.State != BreakerDisposed {
		t.Fatal(snapshot, err)
	}
	if engine.registry.HostInstance() == e.registry.HostInstance() {
		t.Fatal("reused registry epoch")
	}
}
func TestBreakerTimeoutResetRetainsCapacity(t *testing.T) {
	clock := newFakeClock()
	d := definition("event.ready", Action)
	d.HandlerTimeout = 10 * time.Millisecond
	e, s := engineFixture(t, d, ExecutionConfig{Clock: clock, MaxActive: 1, MaxActivePerOwner: 1, Breaker: BreakerConfig{FailureThreshold: 1}})
	release, finished := make(chan struct{}), make(chan struct{})
	action(t, s, d, "stuck", Options{}, func(context.Context, Invocation) error { <-release; close(finished); return nil })
	if _, err := emit(t, e, d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if snapshot := breakerState(t, e); snapshot.State != BreakerOpen || snapshot.ConsecutiveFailures != 1 {
		t.Fatal(snapshot)
	}
	if err := e.ResetBreaker("fixture-owner", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := emit(t, e, d); !errors.Is(err, ErrUnavailable) {
		t.Fatal("reset released capacity", err)
	}
	if snapshot := breakerState(t, e); snapshot.State != BreakerClosed || snapshot.ConsecutiveFailures != 0 {
		t.Fatal(snapshot)
	}
	close(release)
	<-finished
	// Dispose waits until actual lifecycle completion, including the late result.
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if breakerState(t, e).State != BreakerDisposed {
		t.Fatal(breakerState(t, e))
	}
}
func TestConcurrentBreakerResetAndLateCompletions(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Parallel
	e, s := engineFixture(t, d, ExecutionConfig{Clock: newFakeClock(), Breaker: BreakerConfig{FailureThreshold: 1}})
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	action(t, s, d, "late", Options{}, func(context.Context, Invocation) error {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
			return errors.New("late failure")
		}
		return errors.New("failure")
	})
	done := make(chan error, 1)
	go func() { _, err := emit(t, e, d); done <- err }()
	<-entered
	_, _ = emit(t, e, d)
	if breakerState(t, e).State != BreakerOpen {
		t.Fatal("not open")
	}
	if err := e.ResetBreaker("fixture-owner", "one"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if snapshot := breakerState(t, e); snapshot.State != BreakerClosed || snapshot.ConsecutiveFailures != 0 {
		t.Fatal("late call reopened reset breaker", snapshot)
	}
}
func TestBreakerCallerAndUnloadCancellationExcluded(t *testing.T) {
	for _, unload := range []bool{false, true} {
		t.Run(fmt.Sprint(unload), func(t *testing.T) {
			d := definition("event.ready", Action)
			e, s := engineFixture(t, d, ExecutionConfig{Clock: newFakeClock(), Breaker: BreakerConfig{FailureThreshold: 1}})
			entered := make(chan struct{})
			action(t, s, d, "handler", Options{}, func(ctx context.Context, _ Invocation) error { close(entered); <-ctx.Done(); return ctx.Err() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { _, _ = e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil); close(done) }()
			<-entered
			if unload {
				if err := s.Dispose(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			<-done
			snapshot := breakerState(t, e)
			if snapshot.ConsecutiveFailures != 0 || snapshot.LastFailureClass != "" {
				t.Fatal(snapshot)
			}
		})
	}
}
