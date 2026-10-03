package pluginhooks

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

// RemoteBatchHandler implements one host writer call, not a JSON-RPC batch.
// Results correspond to requests in order and are validated independently.
// A transport error affects every sent item; no item is retried by the core.
type RemoteBatchHandler interface {
	HandleBatch(context.Context, []RemoteRequest) ([]RemoteResult, error)
}
type RemoteBatchHandlerFunc func(context.Context, []RemoteRequest) ([]RemoteResult, error)

func (f RemoteBatchHandlerFunc) HandleBatch(ctx context.Context, r []RemoteRequest) ([]RemoteResult, error) {
	return f(ctx, r)
}

// RemoteBatchItem targets an opaque remote registration on a shared connection.
// The host owns that handle; a hook name or plugin-supplied scope is not a grant.
type RemoteBatchItem struct {
	Handle   Handle
	Payload  json.RawMessage
	Metadata map[string]string
}
type RemoteBatchOutcome struct {
	Result DispatchResult
	Error  error
}

type batchCall struct {
	ctx      context.Context
	request  RemoteRequest
	ready    chan batchReply
	estimate time.Duration
}
type batchReply struct {
	result RemoteResult
	err    error
}
type batchSlot struct {
	arrived  chan *batchCall
	abandon  chan struct{}
	estimate time.Duration
}

func (s batchSlot) Handle(ctx context.Context, r RemoteRequest) (RemoteResult, error) {
	call := &batchCall{ctx: ctx, request: r, ready: make(chan batchReply, 1), estimate: s.estimate}
	s.arrived <- call
	// The outer execution waiter handles deadlines; the lease remains held until
	// the shared transport actually returns, even if this item's caller expires.
	select {
	case reply := <-call.ready:
		return reply.result, reply.err
	case <-s.abandon:
		return RemoteResult{}, context.DeadlineExceeded
	}
}

// prepareRemoteBatch validates the entire envelope before admitting any item. It
// sends only observation actions on one connection, never gates, filters or
// notifications. Cap is min(64, each declaration's optional lowered batch cap).
// Runtime capacity/once/breaker/timeout failures are independent ordered results.
// Capacity admission is nonblocking to avoid holding one lease while awaiting
// another lease whose shared transport has not yet started.
func (e *Engine) prepareRemoteBatch(ctx context.Context, handler RemoteBatchHandler, items []RemoteBatchItem) ([]*dispatch, []*entry, error) {
	if handler == nil || len(items) < 1 || len(items) > 64 {
		return nil, nil, ErrInvalidOptions
	}
	prepared := make([]*dispatch, len(items))
	entries := make([]*entry, len(items))
	cleanup := func() {
		for _, d := range prepared {
			if d != nil {
				d.cleanup()
			}
		}
	}
	var connection *RemoteConnection
	for i, item := range items {
		e.registry.mu.Lock()
		candidate := e.registry.entries[item.Handle.id]
		valid := item.Handle.registry == e.registry && candidate != nil && !candidate.removed && !candidate.scope.disposed && item.Handle == candidate.registration.Handle
		e.registry.mu.Unlock()
		if !valid {
			cleanup()
			return nil, nil, &RemoteFailure{Code: StaleScope, admission: true}
		}
		def := e.registry.definitions[candidate.registration.Hook]
		batchLimit := def.RemoteBatchMax
		if batchLimit == 0 {
			batchLimit = 64
		}
		if candidate.remote == nil || candidate.remote.Notifier != nil || !*def.RemoteOK || def.Kind != Action || (def.Mode != Sequential && def.Mode != Parallel && def.Mode != Async && def.Mode != AfterCommit) || len(items) > batchLimit {
			cleanup()
			return nil, nil, &RemoteFailure{Code: RemoteNotAllowed, admission: true}
		}
		if connection == nil {
			connection = candidate.remote.Connection
		}
		if candidate.remote.Connection != connection || !connection.live() {
			cleanup()
			return nil, nil, &RemoteFailure{Code: StaleBinding, admission: true}
		}
		d, err := e.prepare(ctx, def.Name, item.Payload, item.Metadata, Action, def.Mode == Async || def.Mode == AfterCommit, candidate)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		d.entries = []*entry{candidate}
		d.batch = true
		prepared[i] = d
		entries[i] = candidate
	}
	for _, d := range prepared {
		if (d.definition.Mode == Async) != (prepared[0].definition.Mode == Async) || (d.definition.Mode == AfterCommit) != (prepared[0].definition.Mode == AfterCommit) {
			cleanup()
			return nil, nil, ErrInvalidOptions
		}
	}
	return prepared, entries, nil
}
func (e *Engine) runRemoteBatch(ctx context.Context, handler RemoteBatchHandler, prepared []*dispatch, entries []*entry) []RemoteBatchOutcome {
	defer cleanupBatch(prepared)
	slots := make([]batchSlot, len(prepared))
	channels := make([]<-chan callResult, len(prepared))
	results := make([]RemoteBatchOutcome, len(prepared))
	// All validation above precedes scheduling. Every admitted item still uses the
	// normal capacity, once, context, connection, breaker and result fences.
	for i, d := range prepared {
		slots[i] = batchSlot{arrived: make(chan *batchCall, 1), abandon: make(chan struct{}), estimate: entries[i].remote.LatencyEstimate}
		d.remoteOverride = slots[i]
		admissionBegan := time.Now()
		payload := d.payload
		if view := entries[i].registration.Options.View; view != "" {
			decoded, _ := decode(payload)
			payload = encode(project(decoded, d.definition.Views[view]))
		}
		ch, err := e.invoke(d, entries[i], payload, nil, admissionBegan)
		channels[i] = ch
		if err != nil {
			results[i] = batchOutcome(d, entries[i], callResult{err: err})
			slots[i].arrived <- nil
		}
	}
	// An invocation can fail before reaching its handler (e.g. elapsed latency).
	// Race its terminal waiter with arrival so a missing slot never deadlocks.
	calls := make([]*batchCall, len(prepared))
	for i := range slots {
		if channels[i] == nil {
			<-slots[i].arrived
			continue
		}
		select {
		case calls[i] = <-slots[i].arrived:
		case out := <-channels[i]:
			results[i] = batchOutcome(prepared[i], entries[i], out)
			channels[i] = nil
			close(slots[i].abandon)
		}
	}
	go sendBatch(ctx, handler, calls)
	for i, ch := range channels {
		if ch != nil {
			out := <-ch
			results[i] = batchOutcome(prepared[i], entries[i], out)
		}
	}
	for i, d := range prepared {
		e.finishDispatch(d, results[i].Result, results[i].Error)
	}
	return results
}
func batchOutcome(d *dispatch, entry *entry, out callResult) RemoteBatchOutcome {
	r := DispatchResult{InvocationID: d.id, Status: Success, Outcomes: []HandlerOutcome{outcome(d, entry, out.err, out.class)}}
	err := out.err
	if err != nil {
		if d.ctx.Err() != nil {
			r.Status = CallerCancelled
		} else if entry.registration.Options.OnError == Open {
			r.Status = CompletedWithOpenErrors
			err = nil
		} else {
			r.Status = FailedClosed
		}
	}
	return RemoteBatchOutcome{Result: r, Error: err}
}
func sendBatch(parent context.Context, handler RemoteBatchHandler, calls []*batchCall) {
	var sent []*batchCall
	var requests []RemoteRequest
	var deadline time.Time
	for _, call := range calls {
		if call == nil {
			continue
		}
		if err := call.ctx.Err(); err != nil {
			call.ready <- batchReply{err: err}
			continue
		}
		if itemDeadline, _ := call.ctx.Deadline(); time.Until(itemDeadline) < call.estimate {
			call.ready <- batchReply{err: &RemoteFailure{Code: LatencyBudgetExceeded, admission: true}}
			continue
		}
		sent = append(sent, call)
		requests = append(requests, call.request)
		d, _ := call.ctx.Deadline()
		if d.After(deadline) {
			deadline = d
		}
	}
	if len(sent) == 0 {
		return
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	var cancelled atomic.Int32
	stops := make([]func() bool, len(sent))
	for i, call := range sent {
		stops[i] = context.AfterFunc(call.ctx, func() {
			if int(cancelled.Add(1)) == len(sent) {
				cancel()
			}
		})
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()
	var results []RemoteResult
	var err error
	func() {
		defer func() {
			if recover() != nil {
				results = make([]RemoteResult, len(sent))
				for i, c := range sent {
					results[i] = RemoteResult{InvocationID: c.request.InvocationID, Status: RemoteFailed, Failure: &RemoteFailure{Code: HandlerPanic}}
				}
			}
		}()
		results, err = handler.HandleBatch(ctx, requests)
	}()
	if err == nil && len(results) != len(sent) {
		results = make([]RemoteResult, len(sent))
		for i, c := range sent {
			results[i] = RemoteResult{InvocationID: c.request.InvocationID, Status: RemoteFailed, Failure: &RemoteFailure{Code: InvalidOutput}}
		}
	}
	for i, call := range sent {
		reply := batchReply{err: err}
		if err == nil {
			reply.result = results[i]
		}
		call.ready <- reply
	}
}
func (e *Engine) tryCapacity(key scopeKey) (func(), error) {
	e.mu.Lock()
	owner := e.owners[key]
	if owner == nil {
		owner = make(chan struct{}, e.config.MaxActivePerOwner)
		e.owners[key] = owner
	}
	e.mu.Unlock()
	select {
	case owner <- struct{}{}:
	default:
		return nil, &RemoteFailure{Code: CapacityExhausted, admission: true}
	}
	select {
	case e.total <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-e.total; <-owner }) }, nil
	default:
		<-owner
		return nil, &RemoteFailure{Code: CapacityExhausted, admission: true}
	}
}

func cleanupBatch(ds []*dispatch) {
	for _, d := range ds {
		d.cleanup()
	}
}

// EmitRemoteBatch runs sequential/parallel observations now. An all-async batch
// enters the engine's bounded queue and returns ordered queued Futures. A mixed
// delivery class is rejected atomically. after_commit requires explicit Commit.
func (e *Engine) EmitRemoteBatch(ctx context.Context, handler RemoteBatchHandler, items []RemoteBatchItem) ([]RemoteBatchOutcome, error) {
	ds, entries, err := e.prepareRemoteBatch(ctx, handler, items)
	if err != nil {
		return nil, err
	}
	if ds[0].definition.Mode == AfterCommit {
		cleanupBatch(ds)
		return nil, ErrCommitRequired
	}
	if ds[0].definition.Mode == Async {
		return e.enqueueRemoteBatch(handler, ds, entries)
	}
	return e.runRemoteBatch(ctx, handler, ds, entries), nil
}
func (e *Engine) enqueueRemoteBatch(handler RemoteBatchHandler, ds []*dispatch, entries []*entry) ([]RemoteBatchOutcome, error) {
	receipts := make([]RemoteBatchOutcome, len(ds))
	for i, d := range ds {
		d.future = &Future{ready: make(chan struct{})}
		receipts[i].Result = DispatchResult{InvocationID: d.id, Status: Queued, Future: d.future}
	}
	ds[0].batchWork = func() {
		results := e.runRemoteBatch(context.WithoutCancel(ds[0].ctx), handler, ds, entries)
		for i, d := range ds {
			d.future.complete(results[i].Result, results[i].Error)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		cleanupBatch(ds)
		return nil, ErrEngineClosed
	}
	select {
	case e.queue <- ds[0]:
		return receipts, nil
	default:
		cleanupBatch(ds)
		return nil, ErrQueueFull
	}
}

// PendingRemoteBatch is a one-shot host confirmation capability. Preparation
// validates/copies every item; transaction wait consumes the original deadlines.
type PendingRemoteBatch struct {
	mu         sync.Mutex
	used       bool
	engine     *Engine
	handler    RemoteBatchHandler
	dispatches []*dispatch
	entries    []*entry
}

func (e *Engine) PrepareRemoteBatchAfterCommit(ctx context.Context, handler RemoteBatchHandler, items []RemoteBatchItem) (*PendingRemoteBatch, error) {
	ds, entries, err := e.prepareRemoteBatch(ctx, handler, items)
	if err != nil {
		return nil, err
	}
	if ds[0].definition.Mode != AfterCommit {
		cleanupBatch(ds)
		return nil, ErrInvalidOptions
	}
	return &PendingRemoteBatch{engine: e, handler: handler, dispatches: ds, entries: entries}, nil
}
func (p *PendingRemoteBatch) Commit() ([]RemoteBatchOutcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return nil, ErrUnavailable
	}
	p.used = true
	return p.engine.enqueueRemoteBatch(p.handler, p.dispatches, p.entries)
}
func (p *PendingRemoteBatch) Rollback() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.used {
		p.used = true
		cleanupBatch(p.dispatches)
	}
}
