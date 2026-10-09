package subprocess

import (
	"context"
	"encoding/json"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"sync"
	"time"
)

// reverseNegotiation owns one connection's immutable offer and narrowed policy.
// It does not interpret host authority, global admission or trusted depth.
type reverseNegotiation struct {
	mu                     sync.RWMutex
	optIn, offered, active bool
	input, output          int
	core                   *correlation
	server                 *server
	admission              *admission
	writer                 *frameWriter
	params                 InitParams
}

func (n *reverseNegotiation) inputLimit() int  { n.mu.RLock(); defer n.mu.RUnlock(); return n.input }
func (n *reverseNegotiation) outputLimit() int { n.mu.RLock(); defer n.mu.RUnlock(); return n.output }
func (n *reverseNegotiation) selected() bool {
	if n == nil {
		return false
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.offered
}
func (n *reverseNegotiation) prepare(ctx context.Context, p InitParams) (context.Context, error) {
	if n == nil || !n.optIn || p.HostServices == nil {
		return ctx, nil
	}
	// Copy through the bounded, validated DTO codec before author Init can mutate it.
	raw, err := marshalBounded(p, n.inputLimit()-1)
	if err != nil {
		return ctx, initInvalid("host_services")
	}
	var snapshot InitParams
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return ctx, err
	}
	l := snapshot.HostServices.Limits
	input, output := min(n.inputLimit(), int(l.MaxFrameBytes)), min(n.outputLimit(), int(l.MaxFrameBytes))
	n.writer.mu.Lock()
	// Existing Init terminal credit and queued bytes keep their accounting. A
	// ceiling too small for already reserved work cannot be acknowledged safely.
	bytes := min(n.writer.limits.Bytes, int(l.MaxQueuedWriteBytes))
	if n.writer.control.bytes+n.writer.control.reservedBytes > bytes || n.writer.ordinary.bytes+n.writer.ordinary.reservedBytes > bytes {
		n.writer.mu.Unlock()
		return ctx, initInvalid("host_services.limits")
	}
	n.writer.limits.Bytes = bytes
	n.writer.timeout = min(n.writer.timeout, time.Duration(l.WriteTimeoutMS)*time.Millisecond)
	n.writer.frameLimit = output
	n.writer.mu.Unlock()
	n.admission.mu.Lock()
	n.admission.limits.Forward = min(n.admission.limits.Forward, int(l.HostToPluginInflight))
	n.admission.limits.Reverse = min(n.admission.limits.Reverse, int(l.PluginToHostInflight))
	n.admission.limits.Control = min(n.admission.limits.Control, int(l.ControlSlots))
	reverse := n.admission.limits.Reverse
	n.admission.mu.Unlock()
	n.core.mu.Lock()
	n.core.methodTimeoutMS = snapshot.HostServices.Limits.MethodTimeoutMS
	n.core.reversePermits = make(chan struct{}, reverse)
	n.core.provisional = true
	n.core.mu.Unlock()
	n.mu.Lock()
	n.input, n.output, n.params, n.offered = input, output, snapshot, true
	n.mu.Unlock()
	return n.client(ctx), nil
}
func (n *reverseNegotiation) clientLocked(ctx context.Context) context.Context {
	scope := scopeFromContext(ctx)
	if !n.offered || scope == nil || scope.binding == nil || !scope.id.positiveInteger() || scope.method == MethodHookHandle || scope.method == MethodHookHandleBatch || (!n.active && scope.method != MethodInit) {
		return ctx
	}
	if lifecycleMethod(scope.method) {
		granted := false
		for _, g := range n.params.Grants {
			if g.Name == capability.LogWrite && g.SchemaVersion == 1 {
				granted = true
				break
			}
		}
		if !granted || n.params.HostServices.Limits.MethodTimeoutMS["host/log"] == 0 {
			return ctx
		}
	}
	client, err := hostClientContext(ctx, n.core, n.params, n.server.secrets, time.Time{})
	if err != nil {
		return ctx
	}
	return client
}
func (n *reverseNegotiation) client(ctx context.Context) context.Context {
	if n == nil {
		return ctx
	}
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.clientLocked(ctx)
}
func (n *reverseNegotiation) activate(id RPCID) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.offered {
		return
	}
	n.core.mu.Lock()
	n.core.directional, n.core.provisional = true, false
	n.core.high, _ = id.Integer()
	n.core.mu.Unlock()
	n.active = true
}
func (n *reverseNegotiation) decline() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.offered || n.active {
		return
	}
	n.offered = false
	n.core.mu.Lock()
	n.core.provisional = false
	ids := make([]int64, 0, len(n.core.pending))
	for id := range n.core.pending {
		ids = append(ids, id)
	}
	n.core.mu.Unlock()
	for _, id := range ids {
		n.core.fail(id, errConnectionClosed)
	}
}
func (c *correlation) readsReplies() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed == nil && (c.directional || c.provisional)
}
func (c *correlation) directionalMode() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.directional }

// acceptedGrants refreshes discovery for lifecycle cleanup contexts, which are
// created after forward admission closes. Authority and bindings stay unchanged.
func (n *reverseNegotiation) acceptedGrants(grants capability.GrantSet) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.offered {
		n.params.Grants = grants.Clone()
	}
}
