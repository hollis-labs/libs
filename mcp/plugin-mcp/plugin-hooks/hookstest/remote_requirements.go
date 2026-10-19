package hookstest

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
)

func remoteDefinition(name string, kind hooks.Kind) hooks.Definition {
	d := definition(name, kind)
	d.RemoteOK = pointer(true)
	d.RemoteLatencyBudget = 200 * time.Millisecond
	return d
}
func (e *env) remoteScope(a Dispatcher, owner, generation string, definitions ...hooks.Definition) RemoteScope {
	e.t.Helper()
	names := make([]string, len(definitions))
	for i, d := range definitions {
		names[i] = d.Name
	}
	scope, err := a.NewScope(hooks.ScopeConfig{Owner: owner, Generation: generation, Hooks: names, Remote: true})
	if err != nil {
		e.t.Fatal(err)
	}
	remote, ok := scope.(RemoteScope)
	if !ok {
		e.t.Fatal("dispatcher scope lacks RemoteScope")
	}
	e.t.Cleanup(func() {
		if err := remote.Dispose(e.context()); err != nil {
			e.t.Error(err)
		}
	})
	return remote
}
func (e *env) remoteRegistration(handler hooks.RemoteHandler) hooks.RemoteRegistration {
	e.t.Helper()
	connection, err := hooks.NewRemoteConnection()
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(connection.Close)
	return hooks.RemoteRegistration{Handler: handler, Connection: connection, LatencyEstimate: time.Millisecond}
}
func (e *env) remoteAction(scope RemoteScope, d hooks.Definition, name string, o hooks.Options, r hooks.RemoteRegistration) Handle {
	e.t.Helper()
	h, err := scope.AddRemoteAction(d.Name, name, o, r)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
func (e *env) remoteFilter(scope RemoteScope, d hooks.Definition, name string, o hooks.Options, r hooks.RemoteRegistration) Handle {
	e.t.Helper()
	h, err := scope.AddRemoteFilter(d.Name, name, o, r)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}
func (e *env) remoteAdmission() {
	d := remoteDefinition("conformance.remote", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.remoteScope(a, "owner", "one", d)
	var calls atomic.Int32
	r := e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		calls.Add(1)
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
	}))
	excessive := r
	excessive.LatencyEstimate = d.RemoteLatencyBudget + time.Nanosecond
	if _, err := s.AddRemoteAction(d.Name, "excessive", hooks.Options{}, excessive); err == nil {
		e.t.Fatal("latency estimate above ceiling admitted")
	}
	local := e.scope(a, "local", "one", d)
	if _, err := local.(RemoteScope).AddRemoteAction(d.Name, "wrong-scope", hooks.Options{}, r); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("local scope admitted remote handler: %v", err)
	}
	if _, err := s.AddAction(d.Name, "in-process", hooks.Options{}, func(context.Context, hooks.Invocation) error { return nil }); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("remote scope admitted in-process handler: %v", err)
	}
	observe := e.remoteAction(s, d, "observe", hooks.Options{}, r)
	short, cancel := context.WithTimeout(e.context(), time.Nanosecond)
	cancel()
	if _, err := a.EmitAction(short, d.Name, json.RawMessage(`{}`), nil); err == nil || calls.Load() != 0 {
		e.t.Fatalf("expired call sent: %v sends=%d", err, calls.Load())
	}
	// A live caller with too little budget must refuse before transport.
	s.Remove(observe)
	r.LatencyEstimate = 100 * time.Millisecond
	e.remoteAction(s, d, "budget-refusal", hooks.Options{}, r)
	ctx, stop := context.WithTimeout(e.context(), 50*time.Millisecond)
	defer stop()
	result, err := a.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if err == nil || result.Status != hooks.FailedClosed || calls.Load() != 0 {
		e.t.Fatalf("remaining latency budget ignored: %+v %v sends=%d", result, err, calls.Load())
	}
	denied := definition("conformance.denied", hooks.Action)
	other := e.new(hooks.ExecutionConfig{}, denied)
	if _, err := other.NewScope(hooks.ScopeConfig{Owner: "owner", Generation: "one", Hooks: []string{denied.Name}, Remote: true}); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("remote_ok bypass: %v", err)
	}
}
func (e *env) remoteResults() {
	for _, status := range []hooks.RemoteStatus{hooks.RemoteCancelled, hooks.RemoteApprovalRequired} {
		d := remoteDefinition("conformance.gate", hooks.Action)
		d.Mode = hooks.Bail
		d.OnErrorDefault = hooks.Open
		a := e.new(hooks.ExecutionConfig{}, d)
		s := e.remoteScope(a, "owner", "one", d)
		var next atomic.Int32
		e.remoteAction(s, d, "veto", hooks.Options{Priority: pointer(0)}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			return hooks.RemoteResult{InvocationID: q.InvocationID, Status: status, Reason: "diagnostic only"}, nil
		})))
		e.remoteAction(s, d, "never", hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			next.Add(1)
			return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
		})))
		result, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
		wantErr := hooks.ErrCancelled
		wantStatus := hooks.Cancelled
		if status == hooks.RemoteApprovalRequired {
			wantErr = hooks.ErrApprovalRequired
			wantStatus = hooks.ApprovalRequired
		}
		if !errors.Is(err, wantErr) || result.Status != wantStatus || next.Load() != 0 {
			e.t.Fatalf("structured veto lost: %+v %v calls=%d", result, err, next.Load())
		}
	}
	for _, test := range []struct {
		name      string
		result    hooks.RemoteResult
		transport error
		want      error
	}{
		{"error-text", hooks.RemoteResult{Status: hooks.RemoteFailed, Failure: &hooks.RemoteFailure{Code: hooks.HandlerError, Message: "hook cancelled"}}, nil, hooks.ErrRemoteHandler},
		{"transport-sentinel", hooks.RemoteResult{}, hooks.ErrCancelled, hooks.ErrTransport},
		{"unknown-status", hooks.RemoteResult{Status: "success"}, nil, hooks.ErrInvalidOutput},
		{"unknown-code", hooks.RemoteResult{Status: hooks.RemoteFailed, Failure: &hooks.RemoteFailure{Code: "cancelled"}}, nil, hooks.ErrInvalidOutput},
		{"action-output", hooks.RemoteResult{Status: hooks.RemoteOK, Payload: json.RawMessage(`null`)}, nil, hooks.ErrInvalidOutput},
		{"non-ok-output", hooks.RemoteResult{Status: hooks.RemoteFailed, Payload: json.RawMessage(`{}`), Failure: &hooks.RemoteFailure{Code: hooks.HandlerError}}, nil, hooks.ErrInvalidOutput},
	} {
		d := remoteDefinition("conformance.gate", hooks.Action)
		d.Mode = hooks.Bail
		d.OnErrorDefault = hooks.Open
		a := e.new(hooks.ExecutionConfig{}, d)
		s := e.remoteScope(a, "owner", "one", d)
		e.remoteAction(s, d, test.name, hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			r := test.result
			r.InvocationID = q.InvocationID
			return r, test.transport
		})))
		result, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
		if err != nil || result.Status != hooks.CompletedWithOpenErrors || len(result.Outcomes) != 1 || !errors.Is(result.Outcomes[0].Error, test.want) {
			e.t.Fatalf("result branch %s: %+v %v", test.name, result, err)
		}
	}
}
func (e *env) remoteFences() {
	d := remoteDefinition("conformance.filter", hooks.Filter)
	d.MutablePaths = []string{"/visible"}
	d.Views = map[string][]string{"public": {"/visible"}}
	d.RequiredView = "public"
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.remoteScope(a, "owner", "one", d)
	var previousID, previousBinding string
	r := e.remoteRegistration(hooks.RemoteHandlerFunc(func(ctx context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		deadline, ok := ctx.Deadline()
		trace, traceOK := hooks.TraceContextFrom(ctx)
		if q.CatalogVersion != "conformance-v1" || q.SchemaDigest != d.SchemaDigest || q.Hook != d.Name || q.Kind != hooks.Filter || q.Mode != hooks.Waterfall || q.Scope.Owner != "owner" || q.Scope.Generation != "one" || q.Scope.HostInstance == "" || q.Scope.RegistrationID == "" || len(q.Context.BindingID) != 64 || q.Context.ConnectionID == "" || q.Context.Timeout <= 0 || q.Context.Timeout > d.RemoteLatencyBudget || q.Context.AggregateBudget < q.Context.Timeout || !ok || !deadline.Equal(q.Context.Deadline) || !traceOK || q.Context.Trace != trace || q.InvocationID == previousID || q.Context.BindingID == previousBinding {
			e.t.Errorf("missing or reused remote context: %+v", q)
		}
		previousID = q.InvocationID
		previousBinding = q.Context.BindingID
		if !reflect.DeepEqual(decode(q.Payload), map[string]any{"visible": float64(1)}) {
			e.t.Errorf("hidden payload leaked: %s", q.Payload)
		}
		q.Payload[0] = 'x'
		q.Metadata["key"] = "changed"
		q.Context.Deadline = time.Now().Add(time.Hour)
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK, Payload: json.RawMessage(`{"visible":2}`)}, nil
	}))
	h := e.remoteFilter(s, d, "mask", hooks.Options{View: "public"}, r)
	input := json.RawMessage(`{"visible":1,"hidden":true}`)
	metadata := map[string]string{"key": "original"}
	for range 2 {
		result, err := a.ApplyFilters(e.context(), d.Name, input, metadata)
		e.success(result, err, 1)
		if !reflect.DeepEqual(decode(result.Value), map[string]any{"visible": float64(2), "hidden": true}) || metadata["key"] != "original" || string(input) != `{"visible":1,"hidden":true}` {
			e.t.Fatalf("private/merged ownership failed: %+v", result)
		}
	}
	s.Remove(h)
	e.remoteFilter(s, d, "wrong-id", hooks.Options{View: "public"}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		return hooks.RemoteResult{InvocationID: "other-invocation", Status: hooks.RemoteOK, Payload: json.RawMessage(`{"visible":3}`)}, nil
	})))
	if result, err := a.ApplyFilters(e.context(), d.Name, input, nil); !errors.Is(err, hooks.ErrInvalidOutput) || result.Value != nil {
		e.t.Fatalf("uncorrelated output accepted: %+v %v", result, err)
	}
	// A closed connection invalidates a late response before filter acceptance.
	late := e.remoteScope(a, "late", "one", d)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.t.Cleanup(func() { once.Do(func() { close(release) }) })
	r = e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		close(started)
		<-release
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK, Payload: json.RawMessage(`{"visible":99}`)}, nil
	}))
	// Clear the malformed registration so this scope's handler starts.
	if err := s.Dispose(e.context()); err != nil {
		e.t.Fatal(err)
	}
	e.remoteFilter(late, d, "late", hooks.Options{View: "public"}, r)
	done := make(chan hooks.DispatchResult, 1)
	ctx := e.context()
	go func() { result, _ := a.ApplyFilters(ctx, d.Name, input, nil); done <- result }()
	e.wait(started)
	r.Connection.Close()
	select {
	case result := <-done:
		if result.Value != nil || result.Status != hooks.FailedClosed {
			e.t.Fatalf("closed binding accepted output %+v", result)
		}
	case <-time.After(e.bound):
		e.t.Fatal("connection fence did not release caller")
	}
	once.Do(func() { close(release) })
	if err := late.Dispose(e.context()); err != nil {
		e.t.Fatal(err)
	}
	if _, err := late.AddRemoteFilter(d.Name, "reload", hooks.Options{View: "public"}, r); !errors.Is(err, hooks.ErrDisposed) {
		e.t.Fatalf("disposed generation admitted: %v", err)
	}
	if _, err := a.NewScope(hooks.ScopeConfig{Owner: "late", Generation: "one", Remote: true, Hooks: []string{d.Name}}); !errors.Is(err, hooks.ErrDuplicate) {
		e.t.Fatalf("generation reused: %v", err)
	}
	fresh := e.remoteScope(a, "late", "two", d)
	freshHandle := e.remoteFilter(fresh, d, "fresh", hooks.Options{View: "public"}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK, Payload: q.Payload}, nil
	})))
	late.Remove(freshHandle)
	result, err := a.ApplyFilters(e.context(), d.Name, input, nil)
	e.success(result, err, 1)

}

type remoteClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *remoteClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *remoteClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func (e *env) remoteBreaker() {
	d := remoteDefinition("conformance.filter", hooks.Filter)
	d.OnErrorDefault = hooks.Open
	clock := &remoteClock{now: time.Now()}
	a := e.new(hooks.ExecutionConfig{Clock: clock, Breaker: hooks.BreakerConfig{FailureThreshold: 1, Cooldown: time.Second}}, d)
	operator, ok := a.(RemoteDispatcher)
	if !ok {
		e.t.Fatal("dispatcher lacks remote breaker operator")
	}
	s := e.remoteScope(a, "owner", "one", d)
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	e.remoteFilter(s, d, "remote", hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		calls.Add(1)
		if fail.Load() {
			return hooks.RemoteResult{}, errors.New("transport lost")
		}
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK, Payload: q.Payload}, nil
	})))
	result, err := a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"value":1}`), nil)
	if err != nil || result.Status != hooks.CompletedWithOpenErrors || !errors.Is(result.Outcomes[0].Error, hooks.ErrTransport) || string(result.Value) != `{"value":1}` {
		e.t.Fatalf("transport open filter: %+v %v", result, err)
	}
	result, err = a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"value":1}`), nil)
	snapshot, stateErr := operator.Breaker("owner", "one")
	if err != nil || stateErr != nil || snapshot.State != hooks.BreakerOpen || calls.Load() != 1 || !errors.Is(result.Outcomes[0].Error, hooks.ErrUnavailable) {
		e.t.Fatalf("remote open breaker bypass: %+v %v state=%+v sends=%d", result, err, snapshot, calls.Load())
	}
	fail.Store(false)
	clock.advance(time.Second)
	result, err = a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"value":1}`), nil)
	e.success(result, err, 1)
	snapshot, stateErr = operator.Breaker("owner", "one")
	if stateErr != nil || snapshot.State != hooks.BreakerClosed || calls.Load() != 2 {
		e.t.Fatalf("half-open remote probe: %+v %v", snapshot, stateErr)
	}
}
