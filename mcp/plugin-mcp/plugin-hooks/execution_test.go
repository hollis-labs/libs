package pluginhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func engineFixture(t *testing.T, d Definition, c ExecutionConfig) (*Engine, *Scope) {
	t.Helper()
	r, err := NewRegistry(Catalog{"1", []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScope(ScopeConfig{Owner: "fixture-owner", Generation: "one", Hooks: []string{d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(r, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return e, s
}
func action(t *testing.T, s *Scope, d Definition, name string, o Options, f ActionFunc) {
	t.Helper()
	if _, err := s.AddAction(d.Name, name, o, f); err != nil {
		t.Fatal(err)
	}
}
func filter(t *testing.T, s *Scope, d Definition, name string, o Options, f FilterFunc) {
	t.Helper()
	if _, err := s.AddFilter(d.Name, name, o, f); err != nil {
		t.Fatal(err)
	}
}
func TestWaterfallPolicies(t *testing.T) {
	for _, policy := range []ErrorPolicy{Open, Closed} {
		t.Run(string(policy), func(t *testing.T) {
			d := definition("value.change", Filter)
			e, s := engineFixture(t, d, ExecutionConfig{})
			filter(t, s, d, "first", Options{}, func(context.Context, Invocation) (json.RawMessage, error) {
				return json.RawMessage(`{"visible":2,"hidden":"keep"}`), nil
			})
			filter(t, s, d, "error", Options{OnError: policy}, func(context.Context, Invocation) (json.RawMessage, error) {
				return json.RawMessage(`{"visible":999}`), errors.New("fixture failure")
			})
			filter(t, s, d, "last", Options{}, func(_ context.Context, v Invocation) (json.RawMessage, error) {
				if !bytes.Contains(v.Payload, []byte(`"visible":2`)) {
					t.Error(string(v.Payload))
				}
				return v.Payload, nil
			})
			input := json.RawMessage(`{"visible":1,"hidden":"keep"}`)
			result, err := e.ApplyFilters(context.Background(), d.Name, input, nil)
			if policy == Closed {
				if err == nil || result.Value != nil || len(result.Outcomes) != 2 || result.Status != FailedClosed {
					t.Fatal(result, err)
				}
			} else {
				if err != nil || result.Status != CompletedWithOpenErrors || !bytes.Contains(result.Value, []byte(`"visible":2`)) || len(result.Outcomes) != 3 {
					t.Fatal(result, err)
				}
			}
			if string(input) != `{"visible":1,"hidden":"keep"}` {
				t.Fatal("input mutated")
			}
		})
	}
}
func TestBailNeverFailsOpen(t *testing.T) {
	for _, sentinel := range []error{ErrCancelled, ErrApprovalRequired} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			d := definition("operation.start", Action)
			d.Mode = Bail
			e, s := engineFixture(t, d, ExecutionConfig{})
			action(t, s, d, "gate", Options{OnError: Open}, func(context.Context, Invocation) error { return errors.Join(errors.New("reason"), sentinel) })
			action(t, s, d, "later", Options{}, func(context.Context, Invocation) error { t.Error("later gate ran"); return nil })
			r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
			want := Cancelled
			if errors.Is(sentinel, ErrApprovalRequired) {
				want = ApprovalRequired
			}
			if !errors.Is(err, sentinel) || r.Status != want || len(r.Outcomes) != 1 {
				t.Fatal(r, err)
			}
		})
	}
}
func TestParallelIsolationAndOutcomeOrder(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Parallel
	e, s := engineFixture(t, d, ExecutionConfig{})
	var ready sync.WaitGroup
	ready.Add(3)
	for _, name := range []string{"first", "second", "third"} {
		action(t, s, d, name, Options{}, func(_ context.Context, v Invocation) error {
			ready.Done()
			ready.Wait()
			v.Payload[0] = 'x'
			v.Metadata["trace"] = "changed"
			return nil
		})
	}
	input := json.RawMessage(`{"nested":{"value":[1,2]}}`)
	metadata := map[string]string{"trace": "original"}
	r, err := e.EmitAction(context.Background(), d.Name, input, metadata)
	if err != nil || r.Status != Success {
		t.Fatal(r, err)
	}
	for i, name := range []string{"first", "second", "third"} {
		if r.Outcomes[i].Name != name {
			t.Fatal(r)
		}
	}
	if input[0] != '{' || metadata["trace"] != "original" {
		t.Fatal("caller data mutated")
	}
}
func TestDeepViewMergeAndImmutableEdits(t *testing.T) {
	d := definition("value.change", Filter)
	d.MutablePaths = []string{"/profile/name", "/items/0/name"}
	d.Views = map[string][]string{"public": {"/profile/name", "/items/0/name"}}
	e, s := engineFixture(t, d, ExecutionConfig{})
	filter(t, s, d, "mask", Options{View: "public"}, func(_ context.Context, v Invocation) (json.RawMessage, error) {
		if bytes.Contains(v.Payload, []byte("secret")) || bytes.Contains(v.Payload, []byte("hidden")) {
			t.Error(string(v.Payload))
		}
		return json.RawMessage(`{"profile":{"name":"new"},"items":[{"name":"new-item"},null]}`), nil
	})
	input := json.RawMessage(`{"profile":{"name":"old","secret":123},"items":[{"name":"old-item","secret":456},{"hidden":true}],"hidden":"retain"}`)
	r, err := e.ApplyFilters(context.Background(), d.Name, input, nil)
	if err != nil || !bytes.Contains(r.Value, []byte(`"secret":123`)) || !bytes.Contains(r.Value, []byte(`"secret":456`)) || !bytes.Contains(r.Value, []byte(`"hidden":true`)) || !bytes.Contains(r.Value, []byte(`"name":"new"`)) {
		t.Fatal(string(r.Value), err)
	}
	s.Remove(s.Registrations()[0].Handle)
	filter(t, s, d, "invent", Options{View: "public"}, func(context.Context, Invocation) (json.RawMessage, error) {
		return json.RawMessage(`{"profile":{"name":"new","secret":999},"items":[{"name":"x"},null]}`), nil
	})
	r, err = e.ApplyFilters(context.Background(), d.Name, input, nil)
	if !errors.Is(err, ErrInvalidOutput) || r.Value != nil {
		t.Fatal(r, err)
	}
}
func TestPanicAndInvalidOutput(t *testing.T) {
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{})
	action(t, s, d, "panic", Options{OnError: Open}, func(context.Context, Invocation) error { panic("private payload") })
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil || r.Status != CompletedWithOpenErrors || !errors.Is(r.Outcomes[0].Error, ErrPanic) {
		t.Fatal(r, err)
	}
	fd := definition("value.change", Filter)
	fe, fs := engineFixture(t, fd, ExecutionConfig{})
	filter(t, fs, fd, "wrong", Options{}, func(context.Context, Invocation) (json.RawMessage, error) {
		return json.RawMessage(`{"immutable":true}`), nil
	})
	r, err = fe.ApplyFilters(context.Background(), fd.Name, json.RawMessage(`{"immutable":false}`), nil)
	if !errors.Is(err, ErrInvalidOutput) || r.Value != nil {
		t.Fatal(r, err)
	}
}
func TestTimeoutRetainsCapacity(t *testing.T) {
	d := definition("event.ready", Action)
	d.HandlerTimeout = 20 * time.Millisecond
	d.Budget = 200 * time.Millisecond
	e, s := engineFixture(t, d, ExecutionConfig{MaxActive: 1, MaxActivePerOwner: 1})
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	action(t, s, d, "wedged", Options{}, func(context.Context, Invocation) error { calls.Add(1); <-release; return nil })
	start := time.Now()
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.DeadlineExceeded) || r.Status != FailedClosed || time.Since(start) > time.Second {
		t.Fatal(r, err)
	}
	r, err = e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, ErrUnavailable) || calls.Load() != 1 {
		t.Fatal(r, err, calls.Load())
	}
}
func TestWholeBudgetCannotSucceedOpen(t *testing.T) {
	d := definition("event.ready", Action)
	d.HandlerTimeout = 20 * time.Millisecond
	d.Budget = 20 * time.Millisecond
	e, s := engineFixture(t, d, ExecutionConfig{})
	release := make(chan struct{})
	defer close(release)
	action(t, s, d, "wedged", Options{OnError: Open}, func(context.Context, Invocation) error { <-release; return nil })
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.DeadlineExceeded) || r.Status != CallerCancelled {
		t.Fatal(r, err)
	}
}
func TestDepthGuard(t *testing.T) {
	d := definition("event.ready", Action)
	d.HandlerTimeout = 200 * time.Millisecond
	e, s := engineFixture(t, d, ExecutionConfig{MaxDepth: 3})
	action(t, s, d, "recursive", Options{}, func(ctx context.Context, v Invocation) error {
		_, err := e.EmitAction(ctx, d.Name, v.Payload, nil)
		return err
	})
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, ErrDepthExceeded) || r.Status != FailedClosed {
		t.Fatal(r, err)
	}
}
func TestAsyncDepthAndDetachedContext(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Async
	e, s := engineFixture(t, d, ExecutionConfig{MaxDepth: 1})
	started := make(chan struct{})
	goOn := make(chan struct{})
	action(t, s, d, "recursive", Options{}, func(ctx context.Context, v Invocation) error {
		close(started)
		<-goOn
		_, err := e.EmitAction(ctx, d.Name, v.Payload, nil)
		return err
	})
	ctx, cancel := context.WithCancel(context.Background())
	receipt, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if err != nil || receipt.Status != Queued {
		t.Fatal(receipt, err)
	}
	<-started
	cancel()
	close(goOn)
	r, err := receipt.Future.Await(context.Background())
	if !errors.Is(err, ErrDepthExceeded) || r.Status != FailedClosed {
		t.Fatal(r, err)
	}
}
func TestQueueOverloadAndShutdown(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Async
	e, s := engineFixture(t, d, ExecutionConfig{QueueCapacity: 1, Workers: 1})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	action(t, s, d, "slow", Options{}, func(ctx context.Context, _ Invocation) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	first, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*Future{first.Future, second.Future} {
		r, err := f.Await(context.Background())
		if err == nil || r.Status == Success {
			t.Fatal(r, err)
		}
	}
	if _, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); !errors.Is(err, ErrEngineClosed) {
		t.Fatal(err)
	}
	close(release)
}
func TestAfterCommitRollbackAndOneShot(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = AfterCommit
	e, s := engineFixture(t, d, ExecutionConfig{})
	var calls atomic.Int32
	action(t, s, d, "observe", Options{}, func(context.Context, Invocation) error { calls.Add(1); return nil })
	pending, err := e.PrepareAfterCommit(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	pending.Rollback()
	if _, commitErr := pending.Commit(); !errors.Is(commitErr, ErrUnavailable) {
		t.Fatal("rollback commit accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("rollback ran")
	}
	pending, err = e.PrepareAfterCommit(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := pending.Commit()
	if err != nil {
		t.Fatal(err)
	}
	r, err := receipt.Future.Await(context.Background())
	if err != nil || r.Status != Success || calls.Load() != 1 {
		t.Fatal(r, err)
	}
	if _, commitErr := pending.Commit(); !errors.Is(commitErr, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); !errors.Is(err, ErrCommitRequired) {
		t.Fatal(err)
	}
}
func TestConcurrentDispatchOnce(t *testing.T) {
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{})
	var calls atomic.Int32
	action(t, s, d, "once", Options{Once: true, OnError: Open}, func(context.Context, Invocation) error { calls.Add(1); return nil })
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { _, _ = e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil) })
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestUnloadDiscardsLateFilterResult(t *testing.T) {
	d := definition("value.change", Filter)
	e, s := engineFixture(t, d, ExecutionConfig{})
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	filter(t, s, d, "late", Options{}, func(context.Context, Invocation) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"visible":999}`), nil
	})
	done := make(chan callResult, 1)
	go func() {
		r, err := e.ApplyFilters(context.Background(), d.Name, json.RawMessage(`{"visible":1}`), nil)
		done <- callResult{payload: r.Value, err: err}
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Dispose(ctx)
	out := <-done
	if !errors.Is(out.err, ErrUnavailable) || out.payload != nil {
		t.Fatal(string(out.payload), out.err)
	}
}

func TestParallelClosedErrorStillCollectsOutcomes(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Parallel
	d.MaxParallelism = 2
	e, s := engineFixture(t, d, ExecutionConfig{})
	for i, name := range []string{"first", "second", "third"} {
		action(t, s, d, name, Options{}, func(context.Context, Invocation) error {
			if i == 0 {
				return errors.New("ordinary failure")
			}
			return nil
		})
	}
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err == nil || r.Status != FailedClosed || len(r.Outcomes) != 3 {
		t.Fatal(r, err)
	}
}
func TestHookParallelismRetainedAfterTimeout(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Parallel
	d.MaxParallelism = 1
	d.HandlerTimeout = 10 * time.Millisecond
	e, s := engineFixture(t, d, ExecutionConfig{})
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	for _, name := range []string{"one", "two"} {
		action(t, s, d, name, Options{OnError: Open}, func(context.Context, Invocation) error { calls.Add(1); <-release; return nil })
	}
	r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil || calls.Load() != 1 || !errors.Is(r.Outcomes[1].Error, ErrUnavailable) {
		t.Fatal(r, err, calls.Load())
	}
}
func TestQueuedUnloadPreventsStart(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Async
	e, s := engineFixture(t, d, ExecutionConfig{Workers: 1})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	action(t, s, d, "queued", Options{}, func(context.Context, Invocation) error { calls.Add(1); close(started); <-release; return nil })
	first, _ := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	<-started
	second, _ := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Dispose(ctx)
	close(release)
	for _, f := range []*Future{first.Future, second.Future} {
		r, err := f.Await(context.Background())
		if !errors.Is(err, ErrUnavailable) || r.Status != FailedClosed {
			t.Fatal(r, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestInputAndOutputValidation(t *testing.T) {
	d := definition("value.change", Filter)
	d.ValidateInput = func(v json.RawMessage) error {
		if !bytes.Contains(v, []byte("visible")) {
			return errors.New("visible required")
		}
		return nil
	}
	d.ValidateOutput = func(v json.RawMessage) error {
		if bytes.Contains(v, []byte(`"visible":"wrong"`)) {
			return errors.New("number required")
		}
		return nil
	}
	e, s := engineFixture(t, d, ExecutionConfig{})
	filter(t, s, d, "invalid", Options{}, func(context.Context, Invocation) (json.RawMessage, error) {
		return json.RawMessage(`{"visible":"wrong"}`), nil
	})
	for _, input := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`not json`), append(json.RawMessage(`{"visible":"`), 0xff, '"', '}')} {
		if _, err := e.ApplyFilters(context.Background(), d.Name, input, nil); !errors.Is(err, ErrInvalidPayload) {
			t.Fatal(err)
		}
	}
	r, err := e.ApplyFilters(context.Background(), d.Name, json.RawMessage(`{"visible":1}`), nil)
	if !errors.Is(err, ErrInvalidOutput) || r.Value != nil {
		t.Fatal(r, err)
	}
}
func TestJSONPointerEscapesAndDeletion(t *testing.T) {
	d := definition("value.change", Filter)
	d.MutablePaths = []string{"/a~1b/~0key"}
	d.Views = map[string][]string{"escaped": {"/a~1b/~0key"}}
	e, s := engineFixture(t, d, ExecutionConfig{})
	filter(t, s, d, "delete", Options{View: "escaped"}, func(context.Context, Invocation) (json.RawMessage, error) { return json.RawMessage(`{"a/b":{}}`), nil })
	r, err := e.ApplyFilters(context.Background(), d.Name, json.RawMessage(`{"a/b":{"~key":1,"private":2}}`), nil)
	if err != nil || bytes.Contains(r.Value, []byte("~key")) || !bytes.Contains(r.Value, []byte("private")) {
		t.Fatal(string(r.Value), err)
	}
}
func TestSuccessfulCompletionNotCanceled(t *testing.T) {
	d := definition("event.ready", Action)
	e, s := engineFixture(t, d, ExecutionConfig{})
	action(t, s, d, "fast", Options{}, func(context.Context, Invocation) error { return nil })
	for range 100 {
		r, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
		if err != nil || r.Status != Success {
			t.Fatal(r, err)
		}
	}
}

func TestRemovalCancelsCapacityWait(t *testing.T) {
	d := definition("event.ready", Action)
	d.Budget = time.Second
	d.HandlerTimeout = time.Second
	e, s := engineFixture(t, d, ExecutionConfig{MaxActive: 1, MaxActivePerOwner: 1})
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	h, err := s.AddAction(d.Name, "blocking", Options{}, func(context.Context, Invocation) error { close(entered); <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, callErr := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
		done <- callErr
	}()
	<-entered
	go func() {
		_, callErr := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
		done <- callErr
	}()
	s.Remove(h)
	for range 2 {
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("removal failed to cancel wait")
		}
	}
}
func TestFutureResultsAreOwned(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = Async
	e, s := engineFixture(t, d, ExecutionConfig{})
	action(t, s, d, "once", Options{}, func(context.Context, Invocation) error { return nil })
	receipt, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := receipt.Future.Await(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.Outcomes[0].Name = "changed"
	again, err := receipt.Future.Await(context.Background())
	if err != nil || again.Outcomes[0].Name != "once" {
		t.Fatal(again, err)
	}
}
func TestCatalogRejectsUnsupportedMode(t *testing.T) {
	d := definition("event.ready", Action)
	d.Mode = "invented"
	if _, err := NewRegistry(Catalog{"1", []Definition{d}}); !errors.Is(err, ErrInvalidDefinition) {
		t.Fatal(err)
	}
}

func TestCanceledIngress(t *testing.T) {
	d := definition("event.ready", Action)
	e, _ := engineFixture(t, d, ExecutionConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.Canceled) || r.Status != CallerCancelled {
		t.Fatal(r, err)
	}
}
