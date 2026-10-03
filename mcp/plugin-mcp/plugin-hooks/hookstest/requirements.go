package hookstest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
)

func (e *env) ordering() {
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "first", "one", d)
	other := e.scope(a, "second", "one", d)
	var seen []string
	add := func(s Scope, name string, priority *int) {
		e.action(s, d, name, hooks.Options{Priority: priority}, func(_ context.Context, v hooks.Invocation) error { seen = append(seen, v.Registration); return nil })
	}
	add(s, "default", nil)
	add(s, "tie-first", pointer(5))
	add(other, "tie-second", pointer(5))
	add(s, "zero", pointer(0))
	add(other, "negative", pointer(-5))
	r, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 5)
	want := []string{"negative", "zero", "tie-first", "tie-second", "default"}
	if !reflect.DeepEqual(seen, want) || !reflect.DeepEqual(names(r), want) {
		e.t.Fatalf("ordering: calls=%v outcomes=%v", seen, names(r))
	}
	for _, o := range r.Outcomes {
		if o.Hook != d.Name || o.Owner == "" || o.Generation != "one" || o.InvocationID == "" {
			e.t.Errorf("missing outcome identity: %+v", o)
		}
	}
}
func (e *env) waterfall() {
	d := definition("conformance.value", hooks.Filter)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	for _, step := range []struct {
		name       string
		priority   int
		want, next string
	}{{"second", 10, `{"n":1}`, `{"n":2}`}, {"first", 0, `{"n":0}`, `{"n":1}`}, {"last", 10, `{"n":2}`, `{"n":3}`}} {
		e.filter(s, d, step.name, hooks.Options{Priority: pointer(step.priority)}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) {
			if !reflect.DeepEqual(decode(v.Payload), decode(json.RawMessage(step.want))) {
				return nil, fmt.Errorf("%s saw %s", step.name, v.Payload)
			}
			return json.RawMessage(step.next), nil
		})
	}
	r, err := a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"n":0}`), nil)
	e.success(r, err, 3)
	if !reflect.DeepEqual(decode(r.Value), decode(json.RawMessage(`{"n":3}`))) || !reflect.DeepEqual(names(r), []string{"first", "second", "last"}) {
		e.t.Fatalf("waterfall %+v", r)
	}
}
func (e *env) parallel() {
	d := definition("conformance.event", hooks.Action)
	d.Mode = hooks.Parallel
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	started := make(chan struct{}, 3)
	release := make([]chan struct{}, 3)
	finished := make([]chan struct{}, 3)
	for i := range 3 {
		release[i] = make(chan struct{})
		finished[i] = make(chan struct{})
		e.t.Cleanup(func() {
			select {
			case <-release[i]:
			default:
				close(release[i])
			}
		})
		e.action(s, d, fmt.Sprintf("handler-%d", i), hooks.Options{Priority: pointer(i)}, func(ctx context.Context, _ hooks.Invocation) error {
			started <- struct{}{}
			select {
			case <-release[i]:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(finished[i])
			return nil
		})
	}
	type result struct {
		r   hooks.DispatchResult
		err error
	}
	done := make(chan result, 1)
	ctx := e.context()
	go func() {
		r, err := a.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
		done <- result{r, err}
	}()
	for range 3 {
		e.wait(started)
	}
	for i := 2; i >= 0; i-- {
		close(release[i])
		e.wait(finished[i])
	}
	select {
	case got := <-done:
		e.success(got.r, got.err, 3)
		if !reflect.DeepEqual(names(got.r), []string{"handler-0", "handler-1", "handler-2"}) {
			e.t.Fatal(names(got.r))
		}
	case <-time.After(e.bound):
		e.t.Fatal("parallel dispatch exceeded bound")
	}
}
func (e *env) policies() {
	failure := errors.New("handler failure")
	for _, kind := range []hooks.Kind{hooks.Action, hooks.Filter} {
		for _, policy := range []hooks.ErrorPolicy{hooks.Open, hooks.Closed} {
			d := definition("conformance.event", kind)
			d.OnErrorDefault = policy
			a := e.new(hooks.ExecutionConfig{}, d)
			s := e.scope(a, string(kind)+string(policy), "one", d)
			var later atomic.Bool
			if kind == hooks.Action {
				e.action(s, d, "bad", hooks.Options{}, func(context.Context, hooks.Invocation) error { return failure })
				e.action(s, d, "later", hooks.Options{}, func(context.Context, hooks.Invocation) error { later.Store(true); return nil })
			} else {
				e.filter(s, d, "bad", hooks.Options{}, func(context.Context, hooks.Invocation) (json.RawMessage, error) {
					return json.RawMessage(`{"n":999}`), failure
				})
				e.filter(s, d, "later", hooks.Options{}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) {
					later.Store(true)
					return v.Payload, nil
				})
			}
			var r hooks.DispatchResult
			var err error
			if kind == hooks.Action {
				r, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{"n":1}`), nil)
			} else {
				r, err = a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"n":1}`), nil)
			}
			if policy == hooks.Open {
				if err != nil || r.Status != hooks.CompletedWithOpenErrors || !later.Load() || len(r.Outcomes) != 2 {
					e.t.Fatalf("open %+v %v", r, err)
				}
				if kind == hooks.Filter && !reflect.DeepEqual(decode(r.Value), decode(json.RawMessage(`{"n":1}`))) {
					e.t.Fatal("open filter accepted failed output")
				}
			} else if !errors.Is(err, failure) || r.Status != hooks.FailedClosed || later.Load() || len(r.Outcomes) != 1 || r.Value != nil {
				e.t.Fatalf("closed %+v %v", r, err)
			}
			if !errors.Is(r.Outcomes[0].Error, failure) {
				e.t.Fatal("outcome lost failure")
			}
		}
	}
}
func (e *env) bail() {
	for _, sentinel := range []error{hooks.ErrCancelled, hooks.ErrApprovalRequired} {
		d := definition("conformance.gate", hooks.Action)
		d.Mode = hooks.Bail
		d.OnErrorDefault = hooks.Open
		a := e.new(hooks.ExecutionConfig{}, d)
		s := e.scope(a, "owner", "one", d)
		var later atomic.Bool
		e.action(s, d, "veto", hooks.Options{}, func(context.Context, hooks.Invocation) error { return errors.Join(errors.New("reason"), sentinel) })
		e.action(s, d, "later", hooks.Options{}, func(context.Context, hooks.Invocation) error { later.Store(true); return nil })
		r, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
		want := hooks.Cancelled
		if errors.Is(sentinel, hooks.ErrApprovalRequired) {
			want = hooks.ApprovalRequired
		}
		if !errors.Is(err, sentinel) || r.Status != want || later.Load() || len(r.Outcomes) != 1 {
			e.t.Fatalf("bail %+v %v", r, err)
		}
	}
}
func (e *env) timeouts() {
	for _, kind := range []hooks.Kind{hooks.Action, hooks.Filter} {
		for _, whole := range []bool{false, true} {
			d := definition("conformance.event", kind)
			d.HandlerTimeout = 40 * time.Millisecond
			if whole {
				d.Budget = 70 * time.Millisecond
				d.OnErrorDefault = hooks.Open
			}
			a := e.new(hooks.ExecutionConfig{}, d)
			scope := e.scope(a, "owner", "one", d)
			for i := range 2 {
				if kind == hooks.Action {
					e.action(scope, d, fmt.Sprint(i), hooks.Options{}, func(ctx context.Context, _ hooks.Invocation) error { <-ctx.Done(); return ctx.Err() })
				} else {
					e.filter(scope, d, fmt.Sprint(i), hooks.Options{}, func(ctx context.Context, _ hooks.Invocation) (json.RawMessage, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					})
				}
			}
			start := time.Now()
			var result hooks.DispatchResult
			var err error
			if kind == hooks.Action {
				result, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
			} else {
				result, err = a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{}`), nil)
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > e.bound {
				e.t.Fatalf("%s timeout %+v %v", kind, result, err)
			}
			if whole {
				if result.Status != hooks.CallerCancelled {
					e.t.Fatalf("whole budget status %s", result.Status)
				}
			} else if result.Status != hooks.FailedClosed || len(result.Outcomes) != 1 {
				e.t.Fatalf("handler timeout %+v", result)
			}
		}
	}

	ctx, cancel := context.WithCancel(e.context())
	cancel()
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	var called atomic.Bool
	e.action(s, d, "later", hooks.Options{}, func(context.Context, hooks.Invocation) error { called.Store(true); return nil })
	r, err := a.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.Canceled) || r.Status != hooks.CallerCancelled || called.Load() {
		e.t.Fatalf("caller cancellation %+v %v", r, err)
	}
}
func (e *env) panics() {
	for _, mode := range []hooks.Mode{hooks.Sequential, hooks.Parallel, hooks.Bail, hooks.Waterfall} {
		kind := hooks.Action
		if mode == hooks.Waterfall {
			kind = hooks.Filter
		}
		d := definition("conformance.event", kind)
		d.Mode = mode
		d.OnErrorDefault = hooks.Open
		a := e.new(hooks.ExecutionConfig{}, d)
		s := e.scope(a, "owner", "one", d)
		var healthy atomic.Bool
		if kind == hooks.Action {
			e.action(s, d, "panic", hooks.Options{}, func(context.Context, hooks.Invocation) error { panic("probe") })
			e.action(s, d, "healthy", hooks.Options{}, func(context.Context, hooks.Invocation) error { healthy.Store(true); return nil })
		} else {
			e.filter(s, d, "panic", hooks.Options{}, func(context.Context, hooks.Invocation) (json.RawMessage, error) { panic("probe") })
			e.filter(s, d, "healthy", hooks.Options{}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) {
				healthy.Store(true)
				return v.Payload, nil
			})
		}
		var r hooks.DispatchResult
		var err error
		if kind == hooks.Action {
			r, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
		} else {
			r, err = a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{}`), nil)
		}
		if err != nil || r.Status != hooks.CompletedWithOpenErrors || !healthy.Load() || len(r.Outcomes) != 2 || !errors.Is(r.Outcomes[0].Error, hooks.ErrPanic) {
			e.t.Fatalf("panic isolation %+v %v", r, err)
		}
	}
}
func (e *env) once() {
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	var calls atomic.Int32
	e.action(s, d, "once", hooks.Options{Once: true, OnError: hooks.Open}, func(context.Context, hooks.Invocation) error { calls.Add(1); return errors.New("attempted failure") })
	canceled, cancel := context.WithCancel(e.context())
	cancel()
	_, err := a.EmitAction(canceled, d.Name, json.RawMessage(`{}`), nil)
	if !errors.Is(err, context.Canceled) {
		e.t.Fatal(err)
	}
	var wg sync.WaitGroup
	ctx := e.context()
	for range 16 {
		wg.Go(func() {
			_, callErr := a.EmitAction(ctx, d.Name, json.RawMessage(`{}`), nil)
			if callErr != nil {
				e.t.Error(callErr)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		e.t.Fatalf("once attempted %d times", calls.Load())
	}
	r, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 0)
}
func (e *env) handles() {
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	other := e.scope(a, "other", "one", d)
	var calls atomic.Int32
	stale := e.action(s, d, "reused", hooks.Options{}, func(context.Context, hooks.Invocation) error { calls.Add(1); return nil })
	other.Remove(stale)
	r, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 1)
	s.Remove(stale)
	e.action(s, d, "reused", hooks.Options{}, func(context.Context, hooks.Invocation) error { calls.Add(10); return nil })
	s.Remove(stale)
	s.Remove(stale)
	r, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 1)
	if calls.Load() != 11 {
		e.t.Fatalf("stale handle removed new registration: %d", calls.Load())
	}
	if disposeErr := s.Dispose(e.context()); disposeErr != nil {
		e.t.Fatal(disposeErr)
	}
	fresh := e.scope(a, "owner", "two", d)
	e.action(fresh, d, "reused", hooks.Options{}, func(context.Context, hooks.Invocation) error { return nil })
	fresh.Remove(stale)
	r, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 1)
	if _, err := a.NewScope(hooks.ScopeConfig{Owner: "owner", Generation: "one", Hooks: []string{d.Name}}); !errors.Is(err, hooks.ErrDuplicate) {
		e.t.Fatalf("disposed generation revived: %v", err)
	}
}
func (e *env) unload() {
	action := definition("conformance.event", hooks.Action)
	filter := definition("conformance.value", hooks.Filter)
	a := e.new(hooks.ExecutionConfig{}, action, filter)
	s := e.scope(a, "owner", "one", action, filter)
	other := e.scope(a, "other", "one", action, filter)
	started, finished := make(chan struct{}), make(chan struct{})
	e.action(s, action, "active", hooks.Options{}, func(ctx context.Context, _ hooks.Invocation) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return nil
	})
	e.filter(s, filter, "owned", hooks.Options{}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) { return v.Payload, nil })
	e.action(other, action, "survivor", hooks.Options{}, func(context.Context, hooks.Invocation) error { return nil })
	done := make(chan struct{})
	ctx := e.context()
	go func() { _, _ = a.EmitAction(ctx, action.Name, json.RawMessage(`{}`), nil); close(done) }()
	e.wait(started)
	if disposeErr := s.Dispose(e.context()); disposeErr != nil {
		e.t.Fatal(disposeErr)
	}
	e.wait(finished)
	e.wait(done)
	if _, err := s.AddAction(action.Name, "new", hooks.Options{}, func(context.Context, hooks.Invocation) error { return nil }); !errors.Is(err, hooks.ErrDisposed) {
		e.t.Fatalf("disposed admission: %v", err)
	}
	r, err := a.EmitAction(e.context(), action.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 1)
	if r.Outcomes[0].Owner != "other" {
		e.t.Fatal("wrong owner survived")
	}
	r, err = a.ApplyFilters(e.context(), filter.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 0)

	// An uncooperative filter must not publish its late output after unload.
	late := e.scope(a, "late", "one", filter)
	began, release := make(chan struct{}), make(chan struct{})
	e.t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	e.filter(late, filter, "late-output", hooks.Options{}, func(context.Context, hooks.Invocation) (json.RawMessage, error) {
		close(began)
		<-release
		return json.RawMessage(`{"n":999}`), nil
	})
	type completion struct {
		result hooks.DispatchResult
		err    error
	}
	doneFilter := make(chan completion, 1)
	ctx = e.context()
	go func() {
		result, dispatchErr := a.ApplyFilters(ctx, filter.Name, json.RawMessage(`{"n":1}`), nil)
		doneFilter <- completion{result, dispatchErr}
	}()
	e.wait(began)
	short, cancel := context.WithTimeout(e.context(), 20*time.Millisecond)
	err = late.Dispose(short)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		e.t.Fatalf("uncooperative unload must be bounded: %v", err)
	}
	close(release)
	if err := late.Dispose(e.context()); err != nil {
		e.t.Fatal(err)
	}
	select {
	case completion := <-doneFilter:
		if completion.err == nil || completion.result.Value != nil {
			e.t.Fatalf("late output survived unload: %+v %v", completion.result, completion.err)
		}
	case <-time.After(e.bound):
		e.t.Fatal("late filter dispatch exceeded bound")
	}
}
func (e *env) depth() {
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{MaxDepth: 3, MaxActive: 32, MaxActivePerOwner: 32}, d)
	s := e.scope(a, "owner", "one", d)
	var calls atomic.Int32
	e.action(s, d, "recursive", hooks.Options{}, func(ctx context.Context, v hooks.Invocation) error {
		calls.Add(1)
		_, err := a.EmitAction(ctx, d.Name, v.Payload, nil)
		return err
	})
	for range 2 {
		before := calls.Load()
		_, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
		if !errors.Is(err, hooks.ErrDepthExceeded) || calls.Load()-before != 3 {
			e.t.Fatalf("depth guard: calls=%d error=%v", calls.Load()-before, err)
		}
	}
}
func (e *env) capacity() {
	d := definition("conformance.event", hooks.Action)
	d.HandlerTimeout = 30 * time.Millisecond
	d.OnErrorDefault = hooks.Open
	a := e.new(hooks.ExecutionConfig{MaxActive: 1, MaxActivePerOwner: 1}, d)
	s := e.scope(a, "owner", "one", d)
	release, started, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var calls atomic.Int32
	e.action(s, d, "uncooperative", hooks.Options{}, func(context.Context, hooks.Invocation) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return nil
	})
	_, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.wait(started)
	_, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if calls.Load() != 1 {
		e.t.Fatal("timeout released execution capacity before handler finished")
	}
	close(release)
	e.wait(finished)
	r, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	e.success(r, err, 1)
	if calls.Load() != 2 {
		e.t.Fatal("completed handler retained capacity")
	}
}
func (e *env) isolation() {
	for _, mode := range []hooks.Mode{hooks.Sequential, hooks.Parallel} {
		d := definition("conformance.event", hooks.Action)
		d.Mode = mode
		a := e.new(hooks.ExecutionConfig{}, d)
		s := e.scope(a, "owner", "one", d)
		input := json.RawMessage(`{"nested":{"items":[1,2]}}`)
		metadata := map[string]string{"trace": "original"}
		var bad atomic.Bool
		for i := range 3 {
			e.action(s, d, fmt.Sprint(i), hooks.Options{}, func(_ context.Context, v hooks.Invocation) error {
				if !bytes.Equal(v.Payload, []byte(`{"nested":{"items":[1,2]}}`)) || v.Metadata["trace"] != "original" {
					bad.Store(true)
				}
				v.Payload[0] = 'x'
				v.Metadata["trace"] = "changed"
				return nil
			})
		}
		r, err := a.EmitAction(e.context(), d.Name, input, metadata)
		e.success(r, err, 3)
		if bad.Load() || input[0] != '{' || metadata["trace"] != "original" {
			e.t.Fatal("caller or handler shared payload/metadata")
		}
	}
	d := definition("conformance.value", hooks.Filter)
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	output := json.RawMessage(`{"nested":{"n":5}}`)
	e.filter(s, d, "first", hooks.Options{}, func(context.Context, hooks.Invocation) (json.RawMessage, error) { return output, nil })
	e.filter(s, d, "bad", hooks.Options{OnError: hooks.Open}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) {
		v.Payload[0] = 'x'
		return nil, errors.New("discard")
	})
	r, err := a.ApplyFilters(e.context(), d.Name, json.RawMessage(`{"nested":{"n":1}}`), nil)
	if err != nil || !reflect.DeepEqual(decode(r.Value), decode(output)) {
		e.t.Fatalf("accepted output isolation: %s %v", r.Value, err)
	}
	output[0] = 'x'
	if r.Value[0] != '{' {
		e.t.Fatal("egress aliases handler output")
	}
	// Ingress must be detached before the first callback can observe caller edits.
	ad := definition("conformance.ingress", hooks.Action)
	b := e.new(hooks.ExecutionConfig{}, ad)
	scope := e.scope(b, "owner", "one", ad)
	entered, release := make(chan struct{}), make(chan struct{})
	e.t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	raw := json.RawMessage(`{"n":1}`)
	meta := map[string]string{"trace": "before"}
	done := make(chan error, 1)
	e.action(scope, ad, "reader", hooks.Options{}, func(ctx context.Context, v hooks.Invocation) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if v.Payload[0] != '{' || v.Metadata["trace"] != "before" {
			return errors.New("ingress aliases caller")
		}
		return nil
	})
	ctx := e.context()
	go func() { _, err := b.EmitAction(ctx, ad.Name, raw, meta); done <- err }()
	e.wait(entered)
	raw[0] = 'x'
	meta["trace"] = "after"
	close(release)
	select {
	case err := <-done:
		if err != nil {
			e.t.Fatal(err)
		}
	case <-time.After(e.bound):
		e.t.Fatal("ingress dispatch exceeded bound")
	}
}
func (e *env) views() {
	d := definition("conformance.value", hooks.Filter)
	d.MutablePaths = []string{"/profile/name", "/items/0/name"}
	d.Views = map[string][]string{"public": {"/profile/name", "/items/0/name"}}
	d.RequiredView = "public"
	a := e.new(hooks.ExecutionConfig{}, d)
	s := e.scope(a, "owner", "one", d)
	if _, err := s.AddFilter(d.Name, "bypass", hooks.Options{}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) { return v.Payload, nil }); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("required view bypass: %v", err)
	}
	token := e.filter(s, d, "mask", hooks.Options{View: "public"}, func(_ context.Context, v hooks.Invocation) (json.RawMessage, error) {
		if bytes.Contains(v.Payload, []byte("secret")) || bytes.Contains(v.Payload, []byte("hidden")) {
			return nil, errors.New("hidden field leaked")
		}
		return json.RawMessage(`{"profile":{"name":"new"},"items":[{"name":"changed"},null]}`), nil
	})
	raw := json.RawMessage(`{"profile":{"name":"old","secret":7},"items":[{"name":"old","secret":8},{"hidden":true}],"hidden":"kept"}`)
	r, err := a.ApplyFilters(e.context(), d.Name, raw, nil)
	e.success(r, err, 1)
	want := json.RawMessage(`{"profile":{"name":"new","secret":7},"items":[{"name":"changed","secret":8},{"hidden":true}],"hidden":"kept"}`)
	if !reflect.DeepEqual(decode(r.Value), decode(want)) {
		e.t.Fatalf("masked merge: %s", r.Value)
	}
	s.Remove(token)
	e.filter(s, d, "invent", hooks.Options{View: "public"}, func(context.Context, hooks.Invocation) (json.RawMessage, error) {
		return json.RawMessage(`{"profile":{"name":"new","secret":99},"items":[{"name":"changed"},null]}`), nil
	})
	r, err = a.ApplyFilters(e.context(), d.Name, raw, nil)
	if !errors.Is(err, hooks.ErrInvalidOutput) || r.Value != nil {
		e.t.Fatalf("hidden mutation accepted: %s %v", r.Value, err)
	}
}
func (e *env) catalog() {
	for _, mutate := range []func(*hooks.Definition){
		func(d *hooks.Definition) { d.Name = "Bad Name" }, func(d *hooks.Definition) { d.RemoteOK = nil }, func(d *hooks.Definition) { d.ValidateInput = nil }, func(d *hooks.Definition) { d.InputSchema = json.RawMessage(`[]`) }, func(d *hooks.Definition) { d.Budget = 0 }, func(d *hooks.Definition) { d.HandlerTimeout = d.Budget + time.Second }, func(d *hooks.Definition) { d.Mode = hooks.Waterfall }, func(d *hooks.Definition) { d.OnErrorDefault = "" }, func(d *hooks.Definition) { d.SchemaDigest = "" }, func(d *hooks.Definition) { d.Name = "plugin.other.subject.event"; d.OwnerNamespace = "owner" }, func(d *hooks.Definition) { d.Deprecated = &hooks.Deprecation{Since: "1"} }, func(d *hooks.Definition) { d.Views = map[string][]string{"bad": {"/bad~2"}} },
	} {
		d := definition("conformance.event", hooks.Action)
		mutate(&d)
		adapter, err := e.factory(hooks.Catalog{Version: "1", Definitions: []hooks.Definition{d}}, hooks.ExecutionConfig{})
		if adapter != nil {
			_ = adapter.Close(e.context())
		}
		if !errors.Is(err, hooks.ErrInvalidDefinition) {
			e.t.Fatalf("invalid catalog accepted: %+v %v", d, err)
		}
	}
	d := definition("conformance.event", hooks.Action)
	a := e.new(hooks.ExecutionConfig{}, d)
	empty := e.scope(a, "empty", "one")
	s := e.scope(a, "owner", "one", d)
	noop := func(context.Context, hooks.Invocation) error { return nil }
	if _, err := empty.AddAction(d.Name, "blocked", hooks.Options{}, noop); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("empty allowlist: %v", err)
	}
	if _, err := s.AddAction("unknown.event", "unknown", hooks.Options{}, noop); !errors.Is(err, hooks.ErrUnknownHook) {
		e.t.Fatalf("unknown hook: %v", err)
	}
	for _, o := range []hooks.Options{{SchemaDigest: "wrong"}, {Timeout: pointer(time.Duration(0))}, {OnError: "unknown"}, {View: "unknown"}} {
		if _, err := s.AddAction(d.Name, "invalid", o, noop); !errors.Is(err, hooks.ErrInvalidOptions) {
			e.t.Fatalf("invalid option accepted: %+v %v", o, err)
		}
	}
	if _, err := a.NewScope(hooks.ScopeConfig{Owner: "remote", Generation: "one", Hooks: []string{d.Name}, Remote: true}); !errors.Is(err, hooks.ErrUnauthorized) {
		e.t.Fatalf("remote policy ignored: %v", err)
	}
	e.action(s, d, "same", hooks.Options{}, noop)
	if _, err := s.AddAction(d.Name, "same", hooks.Options{}, noop); !errors.Is(err, hooks.ErrDuplicate) {
		e.t.Fatalf("duplicate name: %v", err)
	}
}
