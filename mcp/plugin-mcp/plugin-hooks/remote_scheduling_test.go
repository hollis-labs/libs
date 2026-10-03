package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testBindings struct {
	mu      sync.Mutex
	binding RemoteBinding
}

func (b *testBindings) ResolveRemoteBinding(context.Context, string, string) (RemoteBinding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.binding, nil
}
func (b *testBindings) save(binding RemoteBinding) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.binding = binding
}
func TestRemoteCallbackDetachedDeadlineAndBindingFence(t *testing.T) {
	table := &testBindings{}
	d := definition("callback.start", Action)
	*d.RemoteOK = true
	d.RemoteLatencyBudget = 100 * time.Millisecond
	child := d
	child.Name = "callback.child"
	child.Mode = Async
	r, err := NewRegistry(Catalog{Version: "test", Definitions: []Definition{d, child}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(r, ExecutionConfig{BindingResolver: table})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
		defer cancelCleanup()
		if shutdownErr := e.Shutdown(cleanup); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	s, err := r.NewScope(ScopeConfig{Owner: "owner", Generation: "one", Remote: true, Hooks: []string{d.Name, child.Name}})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := NewRemoteConnection()
	t.Cleanup(connection.Close)
	seen := make(chan RemoteRequest, 1)
	_, err = s.AddRemoteAction(child.Name, "child", Options{}, RemoteRegistration{Connection: connection, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(_ context.Context, q RemoteRequest) (RemoteResult, error) {
		seen <- q
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	var future *Future
	var parentDeadline time.Time
	_, err = s.AddRemoteAction(d.Name, "root", Options{}, RemoteRegistration{Connection: connection, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(_ context.Context, q RemoteRequest) (RemoteResult, error) {
		table.save(q.Binding)
		parentDeadline = q.Context.Deadline
		if _, stop, wrongErr := e.RemoteCallbackContext(ctx, "wrong", q.Binding.ID()); wrongErr == nil {
			stop()
			t.Error("wrong connection admitted")
		}
		nested, stop, bindingErr := e.RemoteCallbackContext(ctx, q.Binding.ConnectionID(), q.Binding.ID())
		if bindingErr != nil {
			return RemoteResult{}, bindingErr
		}
		defer stop()
		receipt, emitErr := e.EmitAction(nested, child.Name, json.RawMessage(`{}`), nil)
		future = receipt.Future
		if emitErr != nil {
			return RemoteResult{}, emitErr
		}
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = future.Await(ctx); err != nil {
		t.Fatal(err)
	}
	q := <-seen
	if q.Context.Depth != 2 || q.Context.Deadline.After(parentDeadline) || q.Context.ParentInvocationID == "" || q.Context.RootInvocationID == "" {
		t.Fatalf("detached callback reset parent %+v", q.Context)
	}
	if _, stop, err := e.RemoteCallbackContext(ctx, q.Binding.ConnectionID(), q.Binding.ID()); err == nil {
		stop()
		t.Fatal("resolver returning wrong/stale binding accepted")
	}
}
func TestRemoteBatchTimeoutRetainsCapacity(t *testing.T) {
	_, e, s, c, d := remoteFixture(t, Action, Sequential)
	timeout := 20 * time.Millisecond
	h, err := s.AddRemoteAction(d.Name, "batch", Options{Timeout: &timeout}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(context.Context, RemoteRequest) (RemoteResult, error) {
		t.Error("unexpected single call")
		return RemoteResult{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	handler := RemoteBatchHandlerFunc(func(_ context.Context, requests []RemoteRequest) ([]RemoteResult, error) {
		calls.Add(1)
		close(started)
		<-release
		out := make([]RemoteResult, len(requests))
		for i, q := range requests {
			out[i] = RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}
		}
		return out, nil
	})
	done := make(chan []RemoteBatchOutcome, 1)
	go func() {
		out, batchErr := e.EmitRemoteBatch(context.Background(), handler, []RemoteBatchItem{{Handle: h, Payload: json.RawMessage(`{}`)}, {Handle: h, Payload: json.RawMessage(`{}`)}})
		if batchErr != nil {
			t.Error(batchErr)
		}
		done <- out
	}()
	<-started
	select {
	case out := <-done:
		if len(out) != 2 || !errors.Is(out[0].Error, context.DeadlineExceeded) || !errors.Is(out[1].Error, ErrUnavailable) {
			t.Fatalf("batch capacity/timeout %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("batch timeout held caller")
	}
	if err = e.ResetBreaker("owner", "one"); err != nil {
		t.Fatal(err)
	}
	out, err := e.EmitRemoteBatch(context.Background(), handler, []RemoteBatchItem{{Handle: h, Payload: json.RawMessage(`{}`)}})
	if err != nil || len(out) != 1 || !errors.Is(out[0].Error, ErrUnavailable) || calls.Load() != 1 {
		t.Fatalf("reset released batch permit %+v %v calls=%d", out, err, calls.Load())
	}
}
func TestRemoteBatchAsyncAndCommit(t *testing.T) {
	for _, mode := range []Mode{Async, AfterCommit} {
		t.Run(string(mode), func(t *testing.T) {
			_, e, s, c, d := remoteFixture(t, Action, mode)
			h, err := s.AddRemoteAction(d.Name, "batch", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(context.Context, RemoteRequest) (RemoteResult, error) {
				t.Error("single-call fallback")
				return RemoteResult{}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			handler := RemoteBatchHandlerFunc(func(_ context.Context, qs []RemoteRequest) ([]RemoteResult, error) {
				calls.Add(1)
				out := make([]RemoteResult, len(qs))
				for i, q := range qs {
					out[i] = RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}
				}
				return out, nil
			})
			items := []RemoteBatchItem{{Handle: h, Payload: json.RawMessage(`{}`)}}
			var receipts []RemoteBatchOutcome
			if mode == AfterCommit {
				if _, err = e.EmitRemoteBatch(context.Background(), handler, items); !errors.Is(err, ErrCommitRequired) {
					t.Fatal(err)
				}
				pending, prepareErr := e.PrepareRemoteBatchAfterCommit(context.Background(), handler, items)
				if prepareErr != nil {
					t.Fatal(prepareErr)
				}
				pending.Rollback()
				if _, err = pending.Commit(); err == nil || calls.Load() != 0 {
					t.Fatal("rollback batch sent")
				}
				pending, err = e.PrepareRemoteBatchAfterCommit(context.Background(), handler, items)
				if err != nil {
					t.Fatal(err)
				}
				items[0].Payload[0] = 'x'
				receipts, err = pending.Commit()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = pending.Commit(); err == nil {
					t.Fatal("batch commit reused")
				}
			} else {
				receipts, err = e.EmitRemoteBatch(context.Background(), handler, items)
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(receipts) != 1 || receipts[0].Result.Status != Queued || receipts[0].Result.Future == nil {
				t.Fatal(receipts)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			final, err := receipts[0].Result.Future.Await(ctx)
			if err != nil || final.Status != Success || calls.Load() != 1 {
				t.Fatalf("queued batch %+v %v", final, err)
			}
		})
	}
}
func TestRemoteNotificationQueueAndNoBreakerSuccess(t *testing.T) {
	d := definition("remote.notify", Action)
	d.Mode = Async
	*d.RemoteOK = true
	d.RemoteLatencyBudget = 200 * time.Millisecond
	d.RemoteFireAndForget = true
	r, err := NewRegistry(Catalog{Version: "test", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(r, ExecutionConfig{Workers: 1, QueueCapacity: 1, Breaker: BreakerConfig{FailureThreshold: 2}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), time.Second)
		defer cancelCleanup()
		if shutdownErr := e.Shutdown(cleanup); shutdownErr != nil {
			t.Error(shutdownErr)
		}
	})
	scope, err := r.NewScope(ScopeConfig{Owner: "owner", Generation: "one", Hooks: []string{d.Name}, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewRemoteConnection()
	t.Cleanup(c.Close)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var sends atomic.Int32
	_, err = scope.AddRemoteAction(d.Name, "notify", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Notifier: RemoteNotifierFunc(func(context.Context, RemoteNotification) error {
		if sends.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	for _, receipt := range []DispatchResult{first, second} {
		final, err := receipt.Future.Await(ctx)
		if err != nil || final.Status != Queued || final.Outcomes[0].Class != "queued" {
			t.Fatalf("notification success fabricated %+v %v", final, err)
		}
	}
	if sends.Load() != 2 {
		t.Fatalf("notification retried %d", sends.Load())
	}
	snapshot, _ := e.Breaker("owner", "one")
	if snapshot.ConsecutiveFailures != 0 {
		t.Fatal(snapshot)
	}
}
func TestRemoteFailureTelemetryUsesClosedCode(t *testing.T) {
	for _, code := range []RemoteFailureCode{CallerCancellation, DeadlineExceeded, DepthExceeded, HandlerPanic, SchemaMismatch} {
		err := &RemoteFailure{Code: code, Message: "success"}
		if got := classify(err); got != string(code) {
			t.Fatalf("%s class %s", code, got)
		}
	}
}

func TestRemoteNotificationHalfOpenRemainsUnproven(t *testing.T) {
	clock := newFakeClock()
	d := definition("remote.probe", Action)
	d.Mode = Async
	*d.RemoteOK = true
	d.RemoteLatencyBudget = 100 * time.Millisecond
	d.RemoteFireAndForget = true
	registry, err := NewRegistry(Catalog{Version: "test", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(registry, ExecutionConfig{Clock: clock, Breaker: BreakerConfig{FailureThreshold: 1, Cooldown: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := e.Shutdown(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	s, err := registry.NewScope(ScopeConfig{Owner: "owner", Generation: "one", Remote: true, Hooks: []string{d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewRemoteConnection()
	t.Cleanup(c.Close)
	fail, err := s.AddRemoteAction(d.Name, "fail", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(context.Context, RemoteRequest) (RemoteResult, error) {
		return RemoteResult{}, errors.New("transport failure")
	})})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receipt.Future.Await(context.Background()); err == nil {
		t.Fatal("failure missing")
	}
	s.Remove(fail)
	var sends atomic.Int32
	_, err = s.AddRemoteAction(d.Name, "notify", Options{}, RemoteRegistration{Connection: c, LatencyEstimate: time.Millisecond, Notifier: RemoteNotifierFunc(func(context.Context, RemoteNotification) error { sends.Add(1); return nil })})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = receipt.Future.Await(context.Background()); !errors.Is(err, ErrUnavailable) || sends.Load() != 0 {
		t.Fatal("open notification sent")
	}
	clock.Advance(time.Second)
	receipt, err = e.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	final, err := receipt.Future.Await(context.Background())
	snapshot, _ := e.Breaker("owner", "one")
	if err != nil || final.Status != Queued || sends.Load() != 1 || snapshot.State != BreakerHalfOpen || snapshot.ProbeActive || snapshot.ConsecutiveFailures != 1 {
		t.Fatalf("notification proved recovery %+v %v state=%+v", final, err, snapshot)
	}
}

func TestRemoteCompletedAncestorIsNotCycle(t *testing.T) {
	table := &testBindings{}
	root := definition("ancestor.root", Action)
	*root.RemoteOK = true
	root.RemoteLatencyBudget = 200 * time.Millisecond
	child := root
	child.Name = "ancestor.child"
	child.Mode = Async
	registry, err := NewRegistry(Catalog{Version: "test", Definitions: []Definition{root, child}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(registry, ExecutionConfig{BindingResolver: table})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := e.Shutdown(cleanup); closeErr != nil {
			t.Error(closeErr)
		}
	})
	s, err := registry.NewScope(ScopeConfig{Owner: "owner", Generation: "one", Remote: true, Hooks: []string{root.Name, child.Name}})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := NewRemoteConnection()
	t.Cleanup(connection.Close)
	parentDone := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(parentDone) })
	var count atomic.Int32
	var future *Future
	_, err = s.AddRemoteAction(root.Name, "root", Options{}, RemoteRegistration{Connection: connection, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(ctx context.Context, q RemoteRequest) (RemoteResult, error) {
		if count.Add(1) == 1 {
			table.save(q.Binding)
			nested, cancel, bindingErr := e.RemoteCallbackContext(ctx, q.Binding.ConnectionID(), q.Binding.ID())
			if bindingErr != nil {
				return RemoteResult{}, bindingErr
			}
			defer cancel()
			receipt, emitErr := e.EmitAction(nested, child.Name, json.RawMessage(`{}`), nil)
			if emitErr != nil {
				return RemoteResult{}, emitErr
			}
			future = receipt.Future
		}
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.AddRemoteAction(child.Name, "child", Options{}, RemoteRegistration{Connection: connection, LatencyEstimate: time.Millisecond, Handler: RemoteHandlerFunc(func(ctx context.Context, q RemoteRequest) (RemoteResult, error) {
		<-parentDone
		table.save(q.Binding)
		nested, cancel, bindingErr := e.RemoteCallbackContext(ctx, q.Binding.ConnectionID(), q.Binding.ID())
		if bindingErr != nil {
			return RemoteResult{}, bindingErr
		}
		defer cancel()
		if _, emitErr := e.EmitAction(nested, root.Name, json.RawMessage(`{}`), nil); emitErr != nil {
			return RemoteResult{}, emitErr
		}
		return RemoteResult{InvocationID: q.InvocationID, Status: RemoteOK}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.EmitAction(context.Background(), root.Name, json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(parentDone) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = future.Await(ctx); err != nil || count.Load() != 2 {
		t.Fatalf("completed ancestor rejected %v calls=%d", err, count.Load())
	}
}
