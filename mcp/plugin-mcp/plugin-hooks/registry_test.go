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

func definition(name string, kind Kind) Definition {
	no := false
	d := Definition{Name: name, Kind: kind, Mode: Sequential, InputSchema: json.RawMessage(`{"type":"object"}`), ValidateInput: func(json.RawMessage) error { return nil }, Since: "0.1.0", RemoteOK: &no, Budget: time.Second, HandlerTimeout: time.Second / 2, OnErrorDefault: Closed, AllowedOnError: []ErrorPolicy{Open, Closed}, MaxPayloadBytes: 1024, MaxHandlers: 100, MaxParallelism: 4, SchemaDigest: "fixture-v1", Views: map[string][]string{"limited": {"/visible"}}}
	if kind == Filter {
		d.Mode = Waterfall
		d.OutputSchema = d.InputSchema
		d.ValidateOutput = d.ValidateInput
		d.MutablePaths = []string{"/visible"}
	}
	return d
}
func fixture(t *testing.T) (*Registry, *Scope) {
	t.Helper()
	r, err := NewRegistry(Catalog{"1", []Definition{definition("event.ready", Action), definition("value.change", Filter)}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScope(ScopeConfig{Owner: "test-owner", Generation: "one", Hooks: []string{"event.ready", "value.change"}})
	if err != nil {
		t.Fatal(err)
	}
	return r, s
}
func add(t *testing.T, s *Scope, name string, o Options) Handle {
	t.Helper()
	h, err := s.AddAction("event.ready", name, o, func(context.Context, Invocation) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func TestPriorityAndStableTies(t *testing.T) {
	r, s := fixture(t)
	zero := 0
	add(t, s, "default", Options{})
	add(t, s, "zero", Options{Priority: &zero})
	add(t, s, "tie", Options{})
	v := s.Registrations()
	for i, n := range []string{"zero", "default", "tie"} {
		if v[i].Name != n {
			t.Fatalf("order: %+v", v)
		}
	}
	for i, e := range r.snapshot("event.ready") {
		if e.registration.Handle != v[i].Handle {
			t.Fatal("snapshot order")
		}
	}
	if v[1].Options.Timeout != time.Second/2 || v[1].Options.OnError != Closed {
		t.Fatal("unresolved defaults")
	}
}
func TestDuplicateNamesAcrossHooks(t *testing.T) {
	_, s := fixture(t)
	add(t, s, "same", Options{})
	_, err := s.AddFilter("value.change", "same", Options{}, func(context.Context, Invocation) (json.RawMessage, error) { return nil, nil })
	if !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}
}
func TestHandlesAndScopedRemoval(t *testing.T) {
	r, s := fixture(t)
	h := add(t, s, "same", Options{})
	other, err := r.NewScope(ScopeConfig{Owner: "other", Generation: "one", Hooks: []string{"event.ready"}})
	if err != nil {
		t.Fatal(err)
	}
	other.Remove(h)
	if len(s.Registrations()) != 1 {
		t.Fatal("foreign removal")
	}
	s.Remove(h)
	s.Remove(h)
	newHandle := add(t, s, "same", Options{})
	if h == newHandle {
		t.Fatal("handle reused")
	}
	r.Remove(h)
	r.Remove(Handle{})
	if len(s.Registrations()) != 1 {
		t.Fatal("stale handle removed replacement")
	}
	r2, _ := fixture(t)
	r2.Remove(newHandle)
	if len(s.Registrations()) != 1 {
		t.Fatal("cross-registry removal")
	}
	r.RemoveByPlugin("test-owner", "wrong")
	if len(s.Registrations()) != 1 {
		t.Fatal("generation ignored")
	}
	r.RemoveByPlugin("test-owner", "one")
	if len(s.Registrations()) != 0 {
		t.Fatal("sweep failed")
	}
	add(t, s, "after-sweep", Options{})
}
func TestRemovalBetweenSnapshotAndStart(t *testing.T) {
	r, s := fixture(t)
	h := add(t, s, "removed", Options{})
	v := r.snapshot("event.ready")
	s.Remove(h)
	if _, err := r.start(context.Background(), v[0]); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	add(t, s, "added", Options{})
	if len(v) != 1 {
		t.Fatal("snapshot changed")
	}
}
func TestConcurrentOnce(t *testing.T) {
	r, s := fixture(t)
	add(t, s, "once", Options{Once: true})
	e := r.snapshot("event.ready")[0]
	var wg sync.WaitGroup
	var n atomic.Int32
	for range 100 {
		wg.Go(func() {
			l, err := r.start(context.Background(), e)
			if err == nil {
				n.Add(1)
				if !l.finish() {
					t.Error("once invalidated its own result")
				}
			}
		})
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatal(n.Load())
	}
	if len(s.Registrations()) != 0 {
		t.Fatal("once still eligible")
	}
}
func TestCanceledStartDoesNotConsumeOnce(t *testing.T) {
	r, s := fixture(t)
	add(t, s, "once", Options{Once: true})
	e := r.snapshot("event.ready")[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.start(ctx, e); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	l, err := r.start(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	l.finish()
}
func TestDisposeCancelsAndBoundsWait(t *testing.T) {
	r, s := fixture(t)
	add(t, s, "active", Options{})
	l, err := r.start(context.Background(), r.snapshot("event.ready")[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Dispose(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-l.ctx.Done():
	default:
		t.Fatal("not canceled")
	}
	if _, err := s.AddAction("event.ready", "later", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrDisposed) {
		t.Fatal(err)
	}
	if l.finish() {
		t.Fatal("late result accepted")
	}
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.NewScope(ScopeConfig{Owner: "test-owner", Generation: "one"}); !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}
	if _, err := r.NewScope(ScopeConfig{Owner: "test-owner", Generation: "two"}); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentDisposalAndRemoval(t *testing.T) {
	r, s := fixture(t)
	h := add(t, s, "active", Options{})
	e := r.snapshot("event.ready")[0]
	l, _ := r.start(context.Background(), e)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { s.Remove(h); r.RemoveByPlugin("test-owner", "one") })
	}
	wg.Go(func() {
		if err := s.Dispose(context.Background()); err != nil {
			t.Error(err)
		}
	})
	l.finish()
	wg.Wait()
	if len(s.Registrations()) != 0 {
		t.Fatal("not disposed")
	}
}
func TestRemoveInvalidatesActiveResult(t *testing.T) {
	r, s := fixture(t)
	h := add(t, s, "active", Options{Once: true})
	l, _ := r.start(context.Background(), r.snapshot("event.ready")[0])
	s.Remove(h)
	if l.finish() {
		t.Fatal("removed result accepted")
	}
	if !errors.Is(l.ctx.Err(), context.Canceled) {
		t.Fatal("not canceled")
	}
}
func TestRegistrationRefusals(t *testing.T) {
	r, s := fixture(t)
	zero := time.Duration(0)
	long := time.Second
	for _, o := range []Options{{Timeout: &zero}, {Timeout: &long}, {OnError: "unknown"}, {View: "unknown"}, {SchemaDigest: "incompatible"}} {
		if _, err := s.AddAction("event.ready", "bad", o, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrInvalidOptions) {
			t.Fatal(o, err)
		}
	}
	if _, err := s.AddAction("unknown.hook", "bad", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnknownHook) {
		t.Fatal(err)
	}
	if _, err := s.AddAction("value.change", "bad", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal(err)
	}
	if _, err := s.AddAction("event.ready", "bad", Options{}, nil); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal(err)
	}
	untrusted, _ := r.NewScope(ScopeConfig{Owner: "restricted", Generation: "one"})
	if _, err := untrusted.AddAction("event.ready", "bad", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := r.NewScope(ScopeConfig{Owner: "remote", Generation: "one", Hooks: []string{"event.ready"}, Remote: true}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
}
func TestCatalogCopiesAndValidation(t *testing.T) {
	d := definition("event.ready", Action)
	r, err := NewRegistry(Catalog{"1", []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	d.InputSchema[0] = 'x'
	d.Views["limited"][0] = "/private"
	*d.RemoteOK = true
	c := r.Catalog()
	if c.Definitions[0].InputSchema[0] != '{' || c.Definitions[0].Views["limited"][0] != "/visible" || *c.Definitions[0].RemoteOK {
		t.Fatal("catalog aliases input")
	}
	c.Definitions[0].Views["limited"][0] = "/changed"
	if r.Catalog().Definitions[0].Views["limited"][0] != "/visible" {
		t.Fatal("catalog aliases output")
	}
	for _, mutate := range []func(*Definition){func(d *Definition) { d.Name = "bare" }, func(d *Definition) { d.Mode = "unknown" }, func(d *Definition) { d.RemoteOK = nil }, func(d *Definition) { d.ValidateInput = nil }, func(d *Definition) { d.InputSchema = json.RawMessage(`null`) }, func(d *Definition) { d.HandlerTimeout = 0 }, func(d *Definition) { d.OnErrorDefault = "" }, func(d *Definition) { d.Views["bad"] = []string{"/~2"} }} {
		bad := definition("event.ready", Action)
		mutate(&bad)
		if _, err := NewRegistry(Catalog{"1", []Definition{bad}}); !errors.Is(err, ErrInvalidDefinition) {
			t.Fatal(err)
		}
	}
}

func TestFilterRegistration(t *testing.T) {
	r, s := fixture(t)
	h, err := s.AddFilter("value.change", "transform", Options{View: "limited"}, func(_ context.Context, v Invocation) (json.RawMessage, error) { return v.Payload, nil })
	if err != nil {
		t.Fatal(err)
	}
	v := r.snapshot("value.change")
	if len(v) != 1 || v[0].registration.Handle != h {
		t.Fatal("missing filter")
	}
	l, err := r.start(context.Background(), v[0])
	if err != nil {
		t.Fatal(err)
	}
	// A handler may use registry methods: plugin code is outside its lock.
	payload, err := v[0].filter(l.ctx, Invocation{Payload: json.RawMessage(`{"visible":1}`)})
	if err != nil || string(payload) != `{"visible":1}` {
		t.Fatal(err, string(payload))
	}
	if !l.finish() {
		t.Fatal("result invalid")
	}
}
func TestHandlerMayRemoveItself(t *testing.T) {
	r, s := fixture(t)
	var h Handle
	var err error
	h, err = s.AddAction("event.ready", "self", Options{}, func(context.Context, Invocation) error { s.Remove(h); return nil })
	if err != nil {
		t.Fatal(err)
	}
	e := r.snapshot("event.ready")[0]
	l, err := r.start(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.action(l.ctx, Invocation{}); err != nil {
		t.Fatal(err)
	}
	if l.finish() {
		t.Fatal("self-removed result accepted")
	}
}

func TestRequiredViewAndHandlerLimit(t *testing.T) {
	d := definition("event.ready", Action)
	d.RequiredView = "limited"
	d.MaxHandlers = 1
	d.AllowedOnError = []ErrorPolicy{Closed}
	r, err := NewRegistry(Catalog{"1", []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScope(ScopeConfig{Owner: "test-owner", Generation: "one", Hooks: []string{d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAction(d.Name, "bad", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, err := s.AddAction(d.Name, "bad", Options{View: "limited", OnError: Open}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal(err)
	}
	h := add(t, s, "allowed", Options{View: "limited"})
	if _, err := s.AddAction(d.Name, "excess", Options{View: "limited"}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	s.Remove(h)
	add(t, s, "replacement", Options{View: "limited"})
}
func TestDisposedOldGenerationDoesNotAffectReload(t *testing.T) {
	r, old := fixture(t)
	h := add(t, old, "same", Options{})
	e := r.snapshot("event.ready")[0]
	l, err := r.start(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = old.Dispose(ctx)
	fresh, err := r.NewScope(ScopeConfig{Owner: "test-owner", Generation: "two", Hooks: []string{"event.ready"}})
	if err != nil {
		t.Fatal(err)
	}
	add(t, fresh, "same", Options{})
	r.Remove(h)
	r.RemoveByPlugin("test-owner", "one")
	if len(fresh.Registrations()) != 1 {
		t.Fatal("reload removed")
	}
	if l.finish() {
		t.Fatal("old result valid")
	}
}
