package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"sync"
)

type ReadonlyQueryArgs struct {
	GrantID, Resource string
	SchemaVersion     uint32
	Params            json.RawMessage
}
type MCPListToolsArgs struct {
	GrantID, ServerID string
	Cursor            *string
}
type MCPCallToolArgs struct {
	GrantID, ServerID, ToolName, ToolBinding string
	Arguments                                json.RawMessage
	OperationKey                             *string
}
type MCPCancelCallArgs struct {
	GrantID string
	Call    *MCPToolCall
}
type BindingsRenewArgs struct {
	GrantID          string
	RequestedLeaseMS uint64
}

func (h *HostClient) ReadonlyQuery(ctx context.Context, a ReadonlyQueryArgs) (r ReadonlyQueryResult, err error) {
	err = h.invoke(ctx, "host/readonly/query", a.GrantID, capability.ReadonlyQuery, func(c ReverseContext) any {
		return ReadonlyQueryParams{a.GrantID, c, a.Resource, a.SchemaVersion, a.Params}
	}, &r)
	if err == nil && (r.Resource != a.Resource || r.SchemaVersion != a.SchemaVersion) {
		h.core.close(errCorrelation)
		return ReadonlyQueryResult{}, localHostFailure(capability.TargetUnavailable)
	}
	return
}
func (h *HostClient) MCPListTools(ctx context.Context, a MCPListToolsArgs) (r MCPListToolsResult, err error) {
	err = h.invoke(ctx, "host/mcp/list_tools", a.GrantID, capability.MCPReach, func(c ReverseContext) any { return MCPListToolsParams{a.GrantID, c, a.ServerID, a.Cursor} }, &r)
	if err == nil && r.ServerID != a.ServerID {
		h.core.close(errCorrelation)
		return MCPListToolsResult{}, localHostFailure(capability.TargetUnavailable)
	}
	return
}

// MCPToolCall is SDK-owned. Start with MCPCallTool, then Wait(ctx); no public ID.
type MCPToolCall struct {
	core    *correlation
	scope   *requestScope
	id      int64
	pending *pendingCall
	done    chan struct{}
	result  MCPCallToolResult
	err     error
}

// Wait's context narrows waiting only; the start context owns call lifetime.
// Use MCPCancelCall for acknowledged host cancellation.
func (c *MCPToolCall) Wait(ctx context.Context) (MCPCallToolResult, error) {
	if c == nil || c.done == nil || ctx == nil {
		return MCPCallToolResult{}, localHostFailure(capability.InvalidRequest)
	}
	select {
	case <-c.done:
		return c.result, c.err
	default:
	}
	select {
	case <-c.done:
		return c.result, c.err
	case <-ctx.Done():
		return MCPCallToolResult{}, requestContextFailure(ctx)
	}
}
func decodeHostCompletion(result callResult, out any) error {
	if result.err != nil {
		var fault *RPCError
		if errors.As(result.err, &fault) && fault.Code == capability.HostRPCErrorCode {
			raw, err := marshalBounded(fault.Data, MaxHostRPCDTOBytes)
			if err != nil {
				return err
			}
			var data HostRPCErrorData
			if err = json.Unmarshal(raw, &data); err != nil {
				return err
			}
			return &HostRPCError{Code: fault.Code, Message: fault.Message, Data: data}
		}
		return result.err
	}
	return json.Unmarshal(result.result, out)
}
func (h *HostClient) MCPCallTool(ctx context.Context, a MCPCallToolArgs) (*MCPToolCall, error) {
	callCtx, cancel, c, err := h.begin(ctx, "host/mcp/call_tool", a.GrantID, capability.MCPReach)
	if err != nil {
		return nil, err
	}
	release := h.scope.retain()
	owned := true
	defer func() {
		if owned {
			cancel()
			release()
		}
	}()
	var key *string
	if a.OperationKey != nil {
		v := *a.OperationKey
		key = &v
	}
	raw, err := marshalBounded(MCPCallToolParams{a.GrantID, c, a.ServerID, a.ToolName, a.ToolBinding, a.Arguments, key}, MaxHostRPCDTOBytes)
	if err != nil {
		return nil, err
	}
	if !h.scope.acceptsResult() {
		return nil, localHostFailure(capability.TargetUnavailable)
	}
	handle := &MCPToolCall{core: h.core, scope: h.scope, done: make(chan struct{})}
	ch, err := h.core.callTracked(callCtx, "host/mcp/call_tool", raw, func(id int64, p *pendingCall) { handle.id = id; handle.pending = p })
	if err != nil {
		return nil, err
	}
	owned = false
	go func() {
		defer cancel()
		defer release()
		result := <-ch
		handle.err = decodeHostCompletion(result, &handle.result)
		if handle.err == nil && ((key == nil) != (handle.result.OperationKey == nil) || key != nil && *key != *handle.result.OperationKey) {
			handle.err = h.badReceipt()
			handle.result = MCPCallToolResult{}
		}
		close(handle.done)
	}()
	return handle, nil
}
func (h *HostClient) MCPCancelCall(ctx context.Context, a MCPCancelCallArgs) (r MCPCancelCallResult, err error) {
	ref := a.Call
	if h == nil || ref == nil || ref.core != h.core || ref.scope != h.scope || ref.pending == nil || ref.id <= 0 {
		return r, localHostFailure(capability.ScopeDenied)
	}
	h.core.mu.Lock()
	current := h.core.pending[ref.id]
	valid := ref.pending.method == "host/mcp/call_tool" && (current == ref.pending || current == nil && ref.pending.possible.Load())
	h.core.mu.Unlock()
	if !valid {
		return r, localHostFailure(capability.ScopeDenied)
	}
	err = h.invoke(ctx, "host/mcp/cancel_call", a.GrantID, capability.MCPReach, func(c ReverseContext) any { return MCPCancelCallParams{a.GrantID, c, uint64(ref.id)} }, &r)
	return
}
func (h *HostClient) BindingsRenew(ctx context.Context, a BindingsRenewArgs) (r BindingsRenewResult, err error) {
	callCtx, cancel, c, err := h.begin(ctx, "host/bindings/renew", a.GrantID, "")
	if err != nil {
		return r, err
	}
	defer cancel()
	if !h.scope.reserveRenew() {
		return r, localHostFailure(capability.RateLimited)
	}
	var once sync.Once
	defer once.Do(h.scope.releaseRenew)
	release := h.scope.retain()
	defer release()
	raw, err := marshalBounded(BindingsRenewParams{a.GrantID, c, a.RequestedLeaseMS}, MaxHostRPCDTOBytes)
	if err != nil {
		return r, err
	}
	if !h.scope.acceptsResult() {
		return r, localHostFailure(capability.TargetUnavailable)
	}
	ch, err := h.core.callContext(callCtx, "host/bindings/renew", raw)
	if err != nil {
		return r, err
	}
	result := <-ch
	if err = decodeHostCompletion(result, &r); err != nil {
		return r, err
	}
	if r.BindingID != c.BindingID {
		return BindingsRenewResult{}, h.badReceipt()
	}
	if !h.scope.applyRenew(r, result.receivedAt, a.RequestedLeaseMS) {
		return BindingsRenewResult{}, localHostFailure(capability.TargetUnavailable)
	}
	return r, nil
}
