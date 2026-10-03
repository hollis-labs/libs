package pluginhooks

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
)

var ErrCallbackCycle = errors.New("hook callback cycle")

type ancestryKey struct{}
type verifiedCallbackKey struct{}
type rootInvocationKey struct{}
type parentInvocationKey struct{}
type activeInvocation struct {
	entry  *entry
	active atomic.Bool
}

// RemoteBinding is an opaque engine-issued parent capability. Hosts retain it in
// their connection-local table while handling a request, and discard it afterward.
// Its zero value, a different connection, or a completed invocation grants nothing.
type RemoteBinding struct{ state *remoteBindingState }
type remoteBindingState struct {
	engine     *Engine
	connection *RemoteConnection
	id         string
	ctx        context.Context
	active     *activeInvocation
}

func (b RemoteBinding) ID() string {
	if b.state == nil {
		return ""
	}
	return b.state.id
}
func (b RemoteBinding) ConnectionID() string {
	if b.state == nil {
		return ""
	}
	return b.state.connection.ID()
}

// RemoteBindingResolver is implemented by the trusted host's connection-local
// binding table. Never reconstruct a binding from plugin-supplied depth or trace.
// Resolve must be bounded and concurrency-safe; the engine revalidates the result.
type RemoteBindingResolver interface {
	ResolveRemoteBinding(context.Context, string, string) (RemoteBinding, error)
}

// RemoteCallbackContext derives a callback's sole depth/deadline/budget/trace
// chain from a verified live parent. Only incoming cancellation/deadline may
// narrow it. Caller values (including trace) cannot replace host parent values.
// The returned cancel function releases the incoming cancellation subscription.
func (e *Engine) RemoteCallbackContext(incoming context.Context, connectionID, bindingID string) (context.Context, context.CancelFunc, error) {
	if e.config.BindingResolver == nil || connectionID == "" || bindingID == "" {
		return nil, nil, &RemoteFailure{Code: StaleBinding, admission: true}
	}
	if err := incoming.Err(); err != nil {
		return nil, nil, err
	}
	b, err := e.config.BindingResolver.ResolveRemoteBinding(incoming, connectionID, bindingID)
	if err != nil {
		return nil, nil, &RemoteFailure{Code: StaleBinding, admission: true}
	}
	s := b.state
	if s == nil || s.engine != e || s.id != bindingID || s.connection.ID() != connectionID || !s.connection.live() || !s.active.active.Load() || s.ctx.Err() != nil {
		return nil, nil, &RemoteFailure{Code: StaleBinding, admission: true}
	}
	ctx, cancel := context.WithCancel(context.WithValue(s.ctx, verifiedCallbackKey{}, true))
	if deadline, ok := incoming.Deadline(); ok {
		clipped, stop := context.WithDeadline(ctx, deadline)
		original := cancel
		cancel = func() { stop(); original() }
		ctx = clipped
	}
	stopIncoming := context.AfterFunc(incoming, cancel)
	return ctx, func() { stopIncoming(); cancel() }, nil
}
func withActiveInvocation(ctx context.Context, entry *entry) (context.Context, *activeInvocation) {
	ancestry, _ := ctx.Value(ancestryKey{}).([]*activeInvocation)
	current := &activeInvocation{entry: entry}
	current.active.Store(true)
	return context.WithValue(ctx, ancestryKey{}, append(slices.Clone(ancestry), current)), current
}
func rejectCycle(ctx context.Context, entries []*entry) error {
	if verified, _ := ctx.Value(verifiedCallbackKey{}).(bool); !verified {
		return nil
	}
	ancestry, _ := ctx.Value(ancestryKey{}).([]*activeInvocation)
	for _, parent := range ancestry {
		if parent.active.Load() {
			for _, candidate := range entries {
				if candidate == parent.entry {
					return &RemoteFailure{Code: CallbackCycle, admission: true}
				}
			}
		}
	}
	return nil
}
