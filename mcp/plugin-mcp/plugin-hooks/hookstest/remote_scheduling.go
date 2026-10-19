package hookstest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	hooks "github.com/hollis-labs/plugin-hooks"
	"sync"
	"sync/atomic"
	"time"
)

type bindingTable struct {
	mu       sync.Mutex
	bindings map[string]hooks.RemoteBinding
}

func (b *bindingTable) store(binding hooks.RemoteBinding) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bindings[binding.ID()] = binding
}
func (b *bindingTable) ResolveRemoteBinding(_ context.Context, connection, id string) (hooks.RemoteBinding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.bindings[id]
	if !ok || v.ConnectionID() != connection {
		return hooks.RemoteBinding{}, hooks.ErrUnavailable
	}
	return v, nil
}
func (e *env) remoteExecution(a Dispatcher) RemoteExecutionDispatcher {
	e.t.Helper()
	r, ok := a.(RemoteExecutionDispatcher)
	if !ok {
		e.t.Fatal("dispatcher lacks RemoteExecutionDispatcher")
	}
	return r
}
func (e *env) remoteAncestry() {
	table := &bindingTable{bindings: map[string]hooks.RemoteBinding{}}
	defs := make([]hooks.Definition, 9)
	for i := range defs {
		defs[i] = remoteDefinition(fmt.Sprintf("conformance.depth%d", i), hooks.Action)
		defs[i].RemoteLatencyBudget = time.Second
		defs[i].HandlerTimeout = time.Second
		defs[i].Budget = 2 * time.Second
	}
	a := e.new(hooks.ExecutionConfig{BindingResolver: table, MaxActive: 32, MaxActivePerOwner: 32}, defs...)
	host := e.remoteExecution(a)
	s := e.remoteScope(a, "depth", "one", defs...)
	var rootID, traceID string
	var parentRequest hooks.RemoteRequest
	var lastBinding hooks.RemoteBinding
	for i, d := range defs {
		index := i
		e.remoteAction(s, d, fmt.Sprintf("depth%d", i), hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			table.store(q.Binding)
			lastBinding = q.Binding
			if q.Context.Depth != index+1 {
				e.t.Errorf("depth %d wanted %d", q.Context.Depth, index+1)
			}
			if index == 0 {
				rootID = q.Context.RootInvocationID
				traceID = q.Context.Trace.TraceID
			} else if q.Context.RootInvocationID != rootID || q.Context.ParentInvocationID != parentRequest.InvocationID || q.Context.Trace.TraceID != traceID || q.Context.Deadline.After(parentRequest.Context.Deadline) || q.Context.AggregateBudget > parentRequest.Context.AggregateBudget {
				e.t.Errorf("parent context reset: %+v", q.Context)
			}
			parentRequest = q
			// A plugin's forged diagnostic request fields are never resolver inputs.
			q.Context.Depth = 0
			q.Context.Trace = hooks.TraceContext{}
			q.Context.Deadline = time.Now().Add(time.Hour)
			incoming, _ := hooks.WithTraceContext(e.context(), hooks.TraceContext{TraceID: "11111111111111111111111111111111", SpanID: "1111111111111111"})
			nested, cancel, err := host.RemoteCallbackContext(incoming, q.Binding.ConnectionID(), q.Binding.ID())
			if err != nil {
				e.t.Error(err)
				return hooks.RemoteResult{}, err
			}
			defer cancel()
			if index < 8 {
				r, err := a.EmitAction(nested, defs[index+1].Name, json.RawMessage(`{}`), nil)
				if index == 7 {
					if !errors.Is(err, hooks.ErrDepthExceeded) || len(r.Outcomes) != 0 {
						e.t.Errorf("ninth dispatch scheduled %+v %v", r, err)
					}
				} else if err != nil {
					e.t.Error(err)
				}
			}
			return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
		})))
	}
	result, err := a.EmitAction(e.context(), defs[0].Name, json.RawMessage(`{}`), nil)
	e.success(result, err, 1)
	if _, cancel, err := host.RemoteCallbackContext(e.context(), lastBinding.ConnectionID(), lastBinding.ID()); err == nil {
		cancel()
		e.t.Fatal("completed parent binding accepted")
	}
	for _, indirect := range []bool{false, true} {
		d1 := remoteDefinition("conformance.cycle", hooks.Action)
		d2 := remoteDefinition("conformance.bridge", hooks.Action)
		r := e.new(hooks.ExecutionConfig{BindingResolver: table}, d1, d2)
		h := e.remoteExecution(r)
		first := e.remoteScope(r, "first", "one", d1)
		second := e.remoteScope(r, "second", "one", d2)
		var entered atomic.Int32
		invoke := func(ctx context.Context, q hooks.RemoteRequest, target string) {
			table.store(q.Binding)
			nested, cancel, err := h.RemoteCallbackContext(ctx, q.Binding.ConnectionID(), q.Binding.ID())
			if err != nil {
				e.t.Error(err)
				return
			}
			defer cancel()
			out, err := r.EmitAction(nested, target, json.RawMessage(`{}`), nil)
			if target == d1.Name {
				if !errors.Is(err, hooks.ErrCallbackCycle) || len(out.Outcomes) != 0 {
					e.t.Errorf("cycle scheduled %+v %v", out, err)
				}
			} else if err != nil {
				e.t.Error(err)
			}
		}
		e.remoteAction(first, d1, "first", hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(ctx context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			if entered.Add(1) > 1 {
				return hooks.RemoteResult{}, errors.New("cycle entered twice")
			}
			target := d1.Name
			if indirect {
				target = d2.Name
			}
			invoke(ctx, q, target)
			return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
		})))
		e.remoteAction(second, d2, "second", hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(ctx context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
			invoke(ctx, q, d1.Name)
			return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
		})))
		out, err := r.EmitAction(e.context(), d1.Name, json.RawMessage(`{}`), nil)
		e.success(out, err, 1)
		if entered.Load() != 1 {
			e.t.Fatal("cycle reached transport")
		}
	}
}
func (e *env) remoteBatches() {
	d := remoteDefinition("conformance.batch", hooks.Action)
	d.RemoteBatchMax = 2
	a := e.new(hooks.ExecutionConfig{}, d)
	host := e.remoteExecution(a)
	s := e.remoteScope(a, "owner", "one", d)
	reg := e.remoteRegistration(hooks.RemoteHandlerFunc(func(_ context.Context, q hooks.RemoteRequest) (hooks.RemoteResult, error) {
		e.t.Error("batch used single-call transport")
		return hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}, nil
	}))
	handle := e.remoteAction(s, d, "batch", hooks.Options{}, reg)
	payload := json.RawMessage(`{"value":1}`)
	metadata := map[string]string{"key": "original"}
	var sends atomic.Int32
	handler := hooks.RemoteBatchHandlerFunc(func(_ context.Context, requests []hooks.RemoteRequest) ([]hooks.RemoteResult, error) {
		sends.Add(1)
		if len(requests) != 2 {
			e.t.Errorf("batch count %d", len(requests))
		}
		out := make([]hooks.RemoteResult, len(requests))
		for i, q := range requests {
			if q.Context.BindingID == "" || q.Context.Timeout <= 0 || q.Context.Deadline.IsZero() || q.Scope.Owner != "owner" || q.Metadata["key"] != "original" {
				e.t.Errorf("missing per-item lease %+v", q)
			}
			if i > 0 && (q.InvocationID == requests[0].InvocationID || q.Context.BindingID == requests[0].Context.BindingID) {
				e.t.Error("shared item identity")
			}
			out[i] = hooks.RemoteResult{InvocationID: q.InvocationID, Status: hooks.RemoteOK}
		}
		requests[0].Payload[0] = 'x'
		requests[0].Metadata["key"] = "changed"
		requests[0].Context.Deadline = time.Now().Add(time.Hour)
		if string(requests[1].Payload) != `{"value":1}` || requests[1].Metadata["key"] != "original" {
			e.t.Error("batch item copies shared")
		}
		out[0] = hooks.RemoteResult{InvocationID: requests[0].InvocationID, Status: hooks.RemoteFailed, Failure: &hooks.RemoteFailure{Code: hooks.HandlerError}}
		return out, nil
	})
	items := []BatchItem{{Handle: handle, Payload: payload, Metadata: metadata}, {Handle: handle, Payload: payload, Metadata: metadata}}
	for _, invalid := range [][]BatchItem{nil, append(append([]BatchItem{}, items...), items[0]), {{Handle: handle, Payload: payload}, {Handle: handle, Payload: json.RawMessage(`{`)}}} {
		if _, err := host.EmitRemoteBatch(e.context(), handler, invalid); err == nil || sends.Load() != 0 {
			e.t.Fatalf("invalid envelope scheduled %v", err)
		}
	}
	out, err := host.EmitRemoteBatch(e.context(), handler, items)
	if err != nil || len(out) != 2 || out[0].Result.Status != hooks.FailedClosed || !errors.Is(out[0].Error, hooks.ErrRemoteHandler) || out[1].Result.Status != hooks.Success || out[1].Error != nil || sends.Load() != 1 {
		e.t.Fatalf("batch independent outcomes %+v %v", out, err)
	}
	if string(payload) != `{"value":1}` || metadata["key"] != "original" {
		e.t.Fatal("batch mutated ingress")
	}
	gate := remoteDefinition("conformance.gate", hooks.Action)
	gate.Mode = hooks.Bail
	g := e.new(hooks.ExecutionConfig{}, gate)
	gs := e.remoteScope(g, "gate", "one", gate)
	gh := e.remoteAction(gs, gate, "gate", hooks.Options{}, e.remoteRegistration(reg.Handler))
	if _, err := e.remoteExecution(g).EmitRemoteBatch(e.context(), handler, []BatchItem{{Handle: gh, Payload: json.RawMessage(`{}`)}}); err == nil {
		e.t.Fatal("gate batched")
	}
}
func (e *env) remoteNotifications() {
	d := remoteDefinition("conformance.notify", hooks.Action)
	d.Mode = hooks.Async
	denied := e.new(hooks.ExecutionConfig{}, d)
	ds := e.remoteScope(denied, "owner", "one", d)
	var sends atomic.Int32
	notify := hooks.RemoteNotifierFunc(func(_ context.Context, n hooks.RemoteNotification) error {
		sends.Add(1)
		if n.Request.Binding.ID() != "" || n.Request.Context.BindingID != "" {
			e.t.Error("notification authorizes callbacks")
		}
		return nil
	})
	reg := e.remoteRegistration(nil)
	reg.Notifier = notify
	if _, err := ds.AddRemoteAction(d.Name, "denied", hooks.Options{}, reg); err == nil {
		e.t.Fatal("notification default accepted")
	}
	d.RemoteFireAndForget = true
	a := e.new(hooks.ExecutionConfig{Breaker: hooks.BreakerConfig{FailureThreshold: 5}}, d)
	host := e.remoteExecution(a)
	s := e.remoteScope(a, "owner", "one", d)
	fail := e.remoteAction(s, d, "fail", hooks.Options{}, e.remoteRegistration(hooks.RemoteHandlerFunc(func(context.Context, hooks.RemoteRequest) (hooks.RemoteResult, error) {
		return hooks.RemoteResult{}, errors.New("failure")
	})))
	receipt, err := a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err = receipt.Future.Await(e.context()); err == nil {
		e.t.Fatal("failure expected")
	}
	s.Remove(fail)
	e.remoteAction(s, d, "notify", hooks.Options{}, reg)
	receipt, err = a.EmitAction(e.context(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil || receipt.Status != hooks.Queued {
		e.t.Fatalf("receipt %+v %v", receipt, err)
	}
	final, err := receipt.Future.Await(e.context())
	snapshot, _ := host.Breaker("owner", "one")
	if err != nil || final.Status != hooks.Queued || len(final.Outcomes) != 1 || final.Outcomes[0].Class != "queued" || final.Outcomes[0].Error != nil || snapshot.ConsecutiveFailures != 1 || sends.Load() != 1 {
		e.t.Fatalf("notification fabricated success %+v %v state=%+v", final, err, snapshot)
	}
	d.Name = "conformance.commit"
	d.Mode = hooks.AfterCommit
	c := e.new(hooks.ExecutionConfig{}, d)
	cs := e.remoteScope(c, "commit", "one", d)
	e.remoteAction(cs, d, "notify", hooks.Options{}, reg)
	pending, err := e.remoteExecution(c).PrepareAfterCommit(e.context(), d.Name, json.RawMessage(`{}`), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	pending.Rollback()
	if _, err = pending.Commit(); err == nil || sends.Load() != 1 {
		e.t.Fatal("rollback sent")
	}
}
