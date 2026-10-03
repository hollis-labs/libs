package pluginhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

var (
	ErrCancelled        = errors.New("hook cancelled")
	ErrApprovalRequired = errors.New("hook approval required")
	ErrDepthExceeded    = errors.New("hook dispatch depth exceeded")
	ErrInvalidPayload   = errors.New("invalid hook payload")
	ErrInvalidOutput    = errors.New("invalid filter output")
	ErrPanic            = errors.New("handler panicked")
	ErrQueueFull        = errors.New("async queue full")
	ErrEngineClosed     = errors.New("execution engine closed")
	ErrCommitRequired   = errors.New("confirmed commit required")
	ErrUnsupportedMode  = errors.New("unsupported hook mode")
)

// Status distinguishes permission decisions, ordinary failures and queue receipts.
type Status string

const (
	Success                 Status = "success"
	Cancelled               Status = "cancelled"
	FailedClosed            Status = "failed_closed"
	CompletedWithOpenErrors Status = "completed_with_open_errors"
	Queued                  Status = "queued"
	ApprovalRequired        Status = "approval_required"
	CallerCancelled         Status = "caller_cancelled"
)

// HandlerOutcome carries identity and classification without exposing payloads.
type HandlerOutcome struct {
	Hook, Owner, Generation, Name, InvocationID string
	Handle                                      Handle
	Error                                       error
	Class                                       string
}

// DispatchResult owns Value and Outcomes. Queued receipts expose a Future.
type DispatchResult struct {
	InvocationID string
	Status       Status
	Value        json.RawMessage
	Outcomes     []HandlerOutcome
	Future       *Future
}

// ExecutionConfig uses documented provisional defaults when fields are zero.
// Limits apply across all dispatches, not just one hook invocation.
type ExecutionConfig struct{ MaxActive, MaxActivePerOwner, MaxDepth, QueueCapacity, Workers int }

// Engine is the sole execution layer for its Registry. Call Shutdown to release
// its worker goroutines. Host validators must be bounded, nonblocking callbacks.
type Engine struct {
	registry *Registry
	config   ExecutionConfig
	total    chan struct{}
	mu       sync.Mutex
	owners   map[scopeKey]chan struct{}
	closed   bool
	lifetime context.Context
	cancel   context.CancelFunc
	queue    chan *dispatch
	workers  sync.WaitGroup
	done     chan struct{}
	ids      atomic.Uint64
}
type depthKey struct{}
type dispatch struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stop       func() bool
	definition Definition
	entries    []*entry
	payload    json.RawMessage
	metadata   map[string]string
	id         string
	future     *Future
	permits    chan struct{}
}

// NewEngine uses provisional defaults: 64 active calls, 8 per owner generation,
// depth 8, 256 queued dispatches and 4 workers. Negative limits are refused.
func NewEngine(r *Registry, c ExecutionConfig) (*Engine, error) {
	if r == nil {
		return nil, ErrInvalidOptions
	}
	values := []*int{&c.MaxActive, &c.MaxActivePerOwner, &c.MaxDepth, &c.QueueCapacity, &c.Workers}
	defaults := []int{64, 8, 8, 256, 4}
	for i, p := range values {
		if *p < 0 {
			return nil, ErrInvalidOptions
		}
		if *p == 0 {
			*p = defaults[i]
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.engine != nil {
		return nil, ErrDuplicate
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{registry: r, config: c, total: make(chan struct{}, c.MaxActive), owners: map[scopeKey]chan struct{}{}, lifetime: ctx, cancel: cancel, queue: make(chan *dispatch, c.QueueCapacity), done: make(chan struct{})}
	r.engine = e
	for range c.Workers {
		e.workers.Go(func() {
			for d := range e.queue {
				result, err := e.execute(d)
				d.future.complete(result, err)
				d.cleanup()
			}
		})
	}
	go func() { e.workers.Wait(); close(e.done) }()
	return e, nil
}

// Shutdown stops admission, cancels calls and drains queued receipts as failures.
// It waits for workers within ctx, but cannot kill uncooperative handler code.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.cancel()
		close(e.queue)
	}
	e.mu.Unlock()
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func cloneMetadata(v map[string]string) map[string]string {
	if v == nil {
		return nil
	}
	out := make(map[string]string, len(v))
	for k, s := range v {
		out[k] = s
	}
	return out
}
func (d *dispatch) cleanup() { d.stop(); d.cancel() }
func (e *Engine) prepare(ctx context.Context, hook string, payload json.RawMessage, metadata map[string]string, kind Kind, detached bool) (*dispatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, ok := e.registry.definitions[hook]
	if !ok {
		return nil, ErrUnknownHook
	}
	if d.Kind != kind {
		return nil, ErrInvalidOptions
	}
	depth, _ := ctx.Value(depthKey{}).(int)
	if depth >= e.config.MaxDepth {
		return nil, ErrDepthExceeded
	}
	if detached {
		ctx = context.WithoutCancel(ctx)
	}
	ctx = context.WithValue(ctx, depthKey{}, depth+1)
	ctx, cancel := context.WithTimeout(ctx, d.Budget)
	stop := context.AfterFunc(e.lifetime, cancel)
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		stop()
		cancel()
		return nil, ErrEngineClosed
	}
	private := bytes.Clone(payload)
	if err := validateJSON(private, d.MaxPayloadBytes, d.ValidateInput); err != nil {
		stop()
		cancel()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		stop()
		cancel()
		return nil, err
	}
	return &dispatch{ctx: ctx, cancel: cancel, stop: stop, definition: d, entries: e.registry.snapshot(hook), payload: private, metadata: cloneMetadata(metadata), id: fmt.Sprintf("inv-%d", e.ids.Add(1)), permits: make(chan struct{}, d.MaxParallelism)}, nil
}

func dispatchFailure(err error) DispatchResult {
	status := FailedClosed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = CallerCancelled
	}
	return DispatchResult{Status: status}
}

// EmitAction observes a private JSON payload or returns an async queue receipt.
// after_commit declarations require PrepareAfterCommit instead.
func (e *Engine) EmitAction(ctx context.Context, hook string, payload json.RawMessage, metadata map[string]string) (DispatchResult, error) {
	def, ok := e.registry.definitions[hook]
	if !ok {
		return DispatchResult{Status: FailedClosed}, ErrUnknownHook
	}
	if def.Mode == AfterCommit {
		return DispatchResult{Status: FailedClosed}, ErrCommitRequired
	}
	d, err := e.prepare(ctx, hook, payload, metadata, Action, def.Mode == Async)
	if err != nil {
		return dispatchFailure(err), err
	}
	if def.Mode == Async {
		return e.enqueue(d)
	}
	defer d.cleanup()
	return e.execute(d)
}

// ApplyFilters returns a successful privately owned value only if the complete
// waterfall succeeds (possibly with explicitly open handler failures).
func (e *Engine) ApplyFilters(ctx context.Context, hook string, payload json.RawMessage, metadata map[string]string) (DispatchResult, error) {
	d, err := e.prepare(ctx, hook, payload, metadata, Filter, false)
	if err != nil {
		return dispatchFailure(err), err
	}
	defer d.cleanup()
	return e.execute(d)
}

// Future exposes the eventual async outcome; receipt alone is never completion.
type Future struct {
	ready  chan struct{}
	result DispatchResult
	err    error
}

func (f *Future) complete(r DispatchResult, err error) { f.result = r; f.err = err; close(f.ready) }
func (f *Future) Await(ctx context.Context) (DispatchResult, error) {
	select {
	case <-ctx.Done():
		return DispatchResult{Status: CallerCancelled}, ctx.Err()
	case <-f.ready:
		r := f.result
		r.Value = bytes.Clone(r.Value)
		r.Outcomes = slices.Clone(r.Outcomes)
		return r, f.err
	}
}
func (e *Engine) enqueue(d *dispatch) (DispatchResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		d.cleanup()
		return DispatchResult{InvocationID: d.id, Status: FailedClosed}, ErrEngineClosed
	}
	d.future = &Future{ready: make(chan struct{})}
	select {
	case e.queue <- d:
		return DispatchResult{InvocationID: d.id, Status: Queued, Future: d.future}, nil
	default:
		d.cleanup()
		return DispatchResult{InvocationID: d.id, Status: FailedClosed}, ErrQueueFull
	}
}

// PendingCommit is a one-shot host capability. Commit must be called only after
// confirmed commit; Rollback discards it. Scheduling is in-memory, not durable.
type PendingCommit struct {
	mu       sync.Mutex
	engine   *Engine
	dispatch *dispatch
	used     bool
}

// PrepareAfterCommit copies and validates now; transaction wait consumes budget.
// Cancellation of the request after preparation does not cancel confirmed work.
func (e *Engine) PrepareAfterCommit(ctx context.Context, hook string, payload json.RawMessage, metadata map[string]string) (*PendingCommit, error) {
	d, err := e.prepare(ctx, hook, payload, metadata, Action, true)
	if err != nil {
		return nil, err
	}
	if d.definition.Mode != AfterCommit {
		d.cleanup()
		return nil, ErrInvalidOptions
	}
	return &PendingCommit{engine: e, dispatch: d}, nil
}
func (p *PendingCommit) Commit() (DispatchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return DispatchResult{Status: FailedClosed}, ErrUnavailable
	}
	p.used = true
	return p.engine.enqueue(p.dispatch)
}
func (p *PendingCommit) Rollback() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.used {
		p.used = true
		p.dispatch.cleanup()
	}
}
func (e *Engine) capacity(ctx context.Context, k scopeKey) (func(), error) {
	e.mu.Lock()
	owner := e.owners[k]
	if owner == nil {
		owner = make(chan struct{}, e.config.MaxActivePerOwner)
		e.owners[k] = owner
	}
	e.mu.Unlock()
	select {
	case owner <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case e.total <- struct{}{}:
		return func() { <-e.total; <-owner }, nil
	case <-ctx.Done():
		<-owner
		return nil, ctx.Err()
	}
}

type callResult struct {
	payload json.RawMessage
	err     error
}

func classify(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrCancelled):
		return "cancelled"
	case errors.Is(err, ErrApprovalRequired):
		return "approval_required"
	case errors.Is(err, ErrPanic):
		return "panic"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "caller_cancelled"
	case errors.Is(err, ErrInvalidOutput):
		return "invalid_output"
	default:
		return "handler_error"
	}
}
func outcome(d *dispatch, entry *entry, err error) HandlerOutcome {
	r := entry.registration
	return HandlerOutcome{Hook: r.Hook, Owner: r.Owner, Generation: r.Generation, Name: r.Name, Handle: r.Handle, InvocationID: d.id, Error: err, Class: classify(err)}
}

// invoke is called in priority admission order. Capacity is reserved synchronously
// before the goroutine and once claim; its release follows actual completion.
func (e *Engine) invoke(d *dispatch, entry *entry, payload json.RawMessage) (<-chan callResult, error) {
	ctx, cancelDeadline := context.WithTimeout(d.ctx, entry.registration.Options.Timeout)
	stopRemoval := context.AfterFunc(entry.removalContext, cancelDeadline)
	cancel := func() { stopRemoval(); cancelDeadline() }
	select {
	case d.permits <- struct{}{}:
	case <-ctx.Done():
		cancel()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, ctx.Err())
	}
	releaseCapacity, err := e.capacity(ctx, entry.scope.key)
	release := func() { releaseCapacity(); <-d.permits }
	if err != nil {
		<-d.permits
		cancel()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	l, err := e.registry.start(ctx, entry)
	if err != nil {
		e.registry.mu.Lock()
		if entry.removed || entry.scope.disposed {
			err = ErrUnavailable
		}
		e.registry.mu.Unlock()
		release()
		cancel()
		return nil, err
	}
	result := make(chan callResult, 1)
	input := bytes.Clone(payload)
	metadata := cloneMetadata(d.metadata)
	go func() {
		out := callResult{}
		func() {
			defer func() {
				if recover() != nil {
					out.err = ErrPanic
				}
			}()
			v := Invocation{ID: d.id, Hook: d.definition.Name, Owner: entry.registration.Owner, Generation: entry.registration.Generation, Registration: entry.registration.Name, Payload: input, Metadata: metadata}
			if entry.action != nil {
				out.err = entry.action(l.ctx, v)
			} else {
				out.payload, out.err = entry.filter(l.ctx, v)
				out.payload = bytes.Clone(out.payload)
			}
		}()
		if ctx.Err() != nil {
			out.payload = nil
			out.err = ctx.Err()
		}
		if !l.finish() {
			out.payload = nil
			out.err = ErrUnavailable
		}
		release()
		result <- out
		cancel()
	}()
	// The bounded waiter releases the caller on timeout/removal while the handler
	// retains its permits. Normal completion cancellation must not mask its result.
	wrapped := make(chan callResult, 1)
	go func() {
		select {
		case out := <-result:
			wrapped <- out
		case <-l.ctx.Done():
			select {
			case out := <-result:
				wrapped <- out
				return
			default:
			}
			err := l.ctx.Err()
			e.registry.mu.Lock()
			removed := entry.removed || entry.scope.disposed
			e.registry.mu.Unlock()
			if removed {
				err = ErrUnavailable
			} else if ctx.Err() == nil || (errors.Is(ctx.Err(), context.Canceled) && d.ctx.Err() == nil) {
				wrapped <- <-result
				return
			}
			wrapped <- callResult{err: err}
		}
	}()
	return wrapped, nil
}
func (e *Engine) execute(d *dispatch) (DispatchResult, error) {
	result := DispatchResult{InvocationID: d.id, Status: Success}
	def := d.definition
	if err := d.ctx.Err(); err != nil {
		return DispatchResult{InvocationID: d.id, Status: CallerCancelled}, err
	}
	switch def.Mode {
	case Parallel:
		return e.parallel(d)
	case Sequential, Bail, Waterfall, Async, AfterCommit:
	default:
		result.Status = FailedClosed
		return result, ErrUnsupportedMode
	}
	current := bytes.Clone(d.payload)
	for _, entry := range d.entries {
		if err := d.ctx.Err(); err != nil {
			result.Status = CallerCancelled
			return result, err
		}
		input := current
		if def.Kind == Action {
			input = d.payload
		}
		full, err := decode(input)
		if err != nil {
			result.Status = FailedClosed
			return result, err
		}
		view := full
		if name := entry.registration.Options.View; name != "" {
			view = project(full, def.Views[name])
		}
		// Projection can share decoded subtrees with full, but only encoded private
		// bytes enter handlers; full and view are never exposed as Go values.
		ch, callErr := e.invoke(d, entry, encode(view))
		out := callResult{err: callErr}
		if callErr == nil {
			select {
			case out = <-ch:
			case <-d.ctx.Done():
				result.Outcomes = append(result.Outcomes, outcome(d, entry, d.ctx.Err()))
				result.Status = CallerCancelled
				return result, d.ctx.Err()
			}
		}
		if def.Kind == Filter && out.err == nil {
			if len(out.payload) > def.MaxPayloadBytes || !utf8.Valid(out.payload) || !json.Valid(out.payload) {
				out.err = ErrInvalidOutput
			} else {
				output, decodeErr := decode(out.payload)
				if decodeErr != nil {
					out.err = ErrInvalidOutput
				} else {
					merged, mergeErr := mergeOutput(full, view, output, def, entry.registration.Options.View)
					if mergeErr != nil {
						out.err = ErrInvalidOutput
					} else {
						accepted := encode(merged)
						if err := validateJSON(accepted, def.MaxPayloadBytes, def.ValidateOutput); err != nil {
							out.err = ErrInvalidOutput
						} else {
							current = bytes.Clone(accepted)
						}
					}
				}
			}
		}
		result.Outcomes = append(result.Outcomes, outcome(d, entry, out.err))
		if err := d.ctx.Err(); err != nil {
			result.Status = CallerCancelled
			return result, err
		}
		if out.err != nil {
			if def.Mode == Bail && errors.Is(out.err, ErrCancelled) {
				result.Status = Cancelled
				return result, ErrCancelled
			}
			if def.Mode == Bail && errors.Is(out.err, ErrApprovalRequired) {
				result.Status = ApprovalRequired
				return result, ErrApprovalRequired
			}
			if entry.registration.Options.OnError == Closed {
				result.Status = FailedClosed
				return result, out.err
			}
			result.Status = CompletedWithOpenErrors
		}
	}
	if def.Kind == Filter {
		result.Value = bytes.Clone(current)
	}
	return result, nil
}
func (e *Engine) parallel(d *dispatch) (DispatchResult, error) {
	result := DispatchResult{InvocationID: d.id, Status: Success}
	type pending struct {
		entry *entry
		ch    <-chan callResult
	}
	var batch []pending
	var closedErr error
	collect := func(p pending) {
		var out callResult
		select {
		case out = <-p.ch:
		case <-d.ctx.Done():
			out.err = d.ctx.Err()
		}
		result.Outcomes = append(result.Outcomes, outcome(d, p.entry, out.err))
		if out.err != nil {
			if p.entry.registration.Options.OnError == Closed {
				if closedErr == nil {
					closedErr = out.err
				}
				result.Status = FailedClosed
			} else if result.Status != FailedClosed {
				result.Status = CompletedWithOpenErrors
			}
		}
	}
	for _, entry := range d.entries {
		if err := d.ctx.Err(); err != nil {
			for _, p := range batch {
				collect(p)
			}
			result.Status = CallerCancelled
			return result, err
		}
		if len(batch) >= d.definition.MaxParallelism {
			collect(batch[0])
			batch = batch[1:]
		}
		input := d.payload
		if name := entry.registration.Options.View; name != "" {
			v, _ := decode(input)
			view := project(v, d.definition.Views[name])
			input = encode(view)
		}
		ch, err := e.invoke(d, entry, input)
		if err != nil {
			chResult := make(chan callResult, 1)
			chResult <- callResult{err: err}
			ch = chResult
		}
		batch = append(batch, pending{entry, ch})
	}
	for _, p := range batch {
		collect(p)
	}
	if err := d.ctx.Err(); err != nil {
		result.Status = CallerCancelled
		return result, err
	}
	return result, closedErr
}
