package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func invokeMCPVector(h *HostClient, ctx context.Context, v hostClientVector, ref *MCPToolCall) (any, error) {
	var a map[string]json.RawMessage
	_ = json.Unmarshal(v.Args, &a)
	a["context"] = json.RawMessage(`{"binding_id":"binding-example","timeout_ms":1000,"parent_call":{"request_owner":"host","id":17}}`)
	raw, _ := json.Marshal(a)
	switch v.Helper {
	case "readonlyQuery":
		type plain ReadonlyQueryParams
		var p plain
		_ = json.Unmarshal(raw, &p)
		return h.ReadonlyQuery(ctx, ReadonlyQueryArgs{p.GrantID, p.Resource, p.SchemaVersion, p.Params})
	case "mcpListTools":
		type plain MCPListToolsParams
		var p plain
		_ = json.Unmarshal(raw, &p)
		return h.MCPListTools(ctx, MCPListToolsArgs{p.GrantID, p.ServerID, p.Cursor})
	case "mcpCallTool":
		type plain MCPCallToolParams
		var p plain
		_ = json.Unmarshal(raw, &p)
		call, err := h.MCPCallTool(ctx, MCPCallToolArgs{p.GrantID, p.ServerID, p.ToolName, p.ToolBinding, p.Arguments, p.OperationKey})
		if err != nil {
			return nil, err
		}
		return call.Wait(ctx)
	case "mcpCancelCall":
		type plain MCPCancelCallParams
		var p plain
		_ = json.Unmarshal(raw, &p)
		return h.MCPCancelCall(ctx, MCPCancelCallArgs{p.GrantID, ref})
	case "bindingsRenew":
		type plain BindingsRenewParams
		var p plain
		_ = json.Unmarshal(raw, &p)
		return h.BindingsRenew(ctx, BindingsRenewArgs{p.GrantID, p.RequestedLeaseMS})
	}
	panic("helper")
}
func TestSharedHostMCPBindingClients(t *testing.T) {
	for _, group := range []string{"readonly", "mcp", "bindings"} {
		raw, err := os.ReadFile("../protocol/v2/fixtures/host-" + group + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Init  InitParams
			Cases []hostClientVector
		}
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, v := range doc.Cases {
			t.Run(v.Name, func(t *testing.T) {
				h, ctx, core, _ := clientFixture(t, doc.Init)
				calls := 0
				var seed *RPCRequest
				core.publish = func(frame []byte, receipt func(error)) error {
					req, fault := decodeEnvelope(frame)
					if fault != nil {
						t.Fatal(fault)
					}
					if v.Helper == "mcpCancelCall" && req.Method == "host/mcp/call_tool" {
						seed = req
						receipt(nil)
						return nil
					}
					calls++
					if req.Method != v.Method {
						t.Fatal("method")
					}
					raw, _ := paramsJSON(req.Params)
					var args map[string]json.RawMessage
					_ = json.Unmarshal(raw, &args)
					var c ReverseContext
					_ = json.Unmarshal(args["context"], &c)
					if c.BindingID != "binding-example" || c.ParentCall.ID != 17 || c.ParentCall.RequestOwner != HostRPCOwnerHost || c.TimeoutMS == 0 || c.TimeoutMS > 1000 {
						t.Fatal("context")
					}
					delete(args, "context")
					b, _ := json.Marshal(args)
					var got, want any
					_ = json.Unmarshal(b, &got)
					_ = json.Unmarshal(v.Args, &want)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("args %s", b)
					}
					response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v.Result}
					if len(v.Error) > 0 {
						delete(response, "result")
						response["error"] = v.Error
					}
					reply, _ := json.Marshal(response)
					if err := core.reply(reply); err != nil {
						core.close(err)
					}
					if seed != nil {
						reply, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": seed.ID, "result": map[string]any{"content": []any{}, "is_error": false}})
						if err := core.reply(reply); err != nil {
							core.close(err)
						}
					}
					receipt(nil)
					return nil
				}
				var ref *MCPToolCall
				if v.Helper == "mcpCancelCall" {
					ref, err = h.MCPCallTool(ctx, MCPCallToolArgs{GrantID: "g-MCPCallTool", ServerID: "example.server", ToolName: "lookup", ToolBinding: "tool-binding-example", Arguments: json.RawMessage(`{}`)})
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := invokeMCPVector(h, ctx, v, ref)
				if strings.HasPrefix(v.ExpectedError, "host:") {
					var fault *HostRPCError
					if !errors.As(err, &fault) || string(fault.Data.Code) != strings.TrimPrefix(v.ExpectedError, "host:") {
						t.Fatalf("host error %v", err)
					}
					return
				}
				if v.ExpectedError != "" {
					var fault *capability.Error
					if !errors.As(err, &fault) || string(fault.Code) != v.ExpectedError {
						t.Fatalf("expected %s: %v", v.ExpectedError, err)
					}
					if v.ExpectedError == "capability_denied" && calls != 0 {
						t.Fatal("denied publication")
					}
					return
				}
				if err != nil || calls != 1 {
					t.Fatalf("result %v calls %d", err, calls)
				}
				b, _ := json.Marshal(got)
				var actual, expected any
				_ = json.Unmarshal(b, &actual)
				_ = json.Unmarshal(v.Result, &expected)
				if !reflect.DeepEqual(actual, expected) {
					t.Fatalf("result %s", b)
				}
				if ref != nil {
					if _, err = ref.Wait(ctx); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func mcpInitFixture(t *testing.T) InitParams {
	t.Helper()
	raw, err := os.ReadFile("../protocol/v2/fixtures/host-mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Init InitParams }
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d.Init
}
func TestMCPHandleWaitAndCancellationOwnership(t *testing.T) {
	h, ctx, core, _ := clientFixture(t, mcpInitFixture(t))
	frames := make(chan *RPCRequest, 4)
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		frames <- req
		done(nil)
		return nil
	}
	call, err := h.MCPCallTool(ctx, MCPCallToolArgs{GrantID: "g-MCPCallTool", ServerID: "example.server", ToolName: "lookup", ToolBinding: "opaque", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	target := <-frames
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = call.Wait(waitCtx); err == nil {
		t.Fatal("cancelled wait")
	}
	core.mu.Lock()
	pending := core.pending[call.id] == call.pending
	core.mu.Unlock()
	if !pending {
		t.Fatal("wait cancelled the tool call")
	}
	for _, ref := range []*MCPToolCall{nil, {}, {core: newCorrelation(true), scope: h.scope, id: call.id, pending: call.pending}} {
		_, err = h.MCPCancelCall(ctx, MCPCancelCallArgs{"g-MCPCancelCall", ref})
		var failure *capability.Error
		if !errors.As(err, &failure) || failure.Code != capability.ScopeDenied {
			t.Fatal("foreign call accepted")
		}
	}
	done := make(chan error, 1)
	go func() { _, err := h.MCPCancelCall(ctx, MCPCancelCallArgs{"g-MCPCancelCall", call}); done <- err }()
	cancelRequest := <-frames
	if cancelRequest.ID == target.ID {
		t.Fatal("cancel request reused target ID")
	}
	raw, _ := paramsJSON(cancelRequest.Params)
	var params MCPCancelCallParams
	_ = json.Unmarshal(raw, &params)
	n, _ := target.ID.Integer()
	if params.TargetCallID != uint64(n) {
		t.Fatal("wrong cancel target")
	}
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": cancelRequest.ID, "result": MCPCancelCallResult{Accepted: true}})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.done:
		t.Fatal("cancel receipt fabricated tool outcome")
	default:
	}
	reply, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": target.ID, "result": map[string]any{"content": []any{}, "is_error": true}})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	result, err := call.Wait(ctx)
	if err != nil || !result.IsError {
		t.Fatal("tool result")
	}
	if _, err = call.Wait(ctx); err != nil {
		t.Fatal("repeat wait")
	}
}
func TestBindingRenewGuardReceiptBudgetAndLateTerminal(t *testing.T) {
	h, ctx, core, scope := clientFixture(t, mcpInitFixture(t))
	frames := make(chan *RPCRequest, 4)
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		frames <- req
		done(nil)
		return nil
	}
	finished := make(chan error, 1)
	go func() { _, err := h.BindingsRenew(ctx, BindingsRenewArgs{"g-storage", 250}); finished <- err }()
	req := <-frames
	_, err := h.BindingsRenew(ctx, BindingsRenewArgs{"g-storage", 250})
	var local *capability.Error
	if !errors.As(err, &local) || local.Code != capability.RateLimited || local.EffectState != capability.NotStarted {
		t.Fatalf("concurrent renew %v", err)
	}
	zero := uint64(0)
	r := BindingsRenewResult{BindingID: "binding-example", ExpiresAt: "2099-01-01T00:00:00Z", RemainingBudgets: HostRPCRemainingBudgets{TimeoutMS: 1000, Bytes: &zero}}
	receipt := time.Now()
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": r})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	scope.mu.Lock()
	lease, budget, guard := scope.leaseEnd, scope.leaseBudgetEnd, scope.renewPending
	scope.mu.Unlock()
	if guard || lease.After(time.Now().Add(250*time.Millisecond)) || lease.Before(receipt) {
		t.Fatal("renew duration/guard")
	}
	requestDone := make(chan error, 1)
	go func() { _, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "k"}); requestDone <- err }()
	next := <-frames
	raw, _ := paramsJSON(next.Params)
	var p StorageGetParams
	_ = json.Unmarshal(raw, &p)
	if p.Context.TimeoutMS == 0 || p.Context.TimeoutMS > 250 {
		t.Fatal("verified lease not clipped")
	}
	reply, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": next.ID, "result": StorageGetResult{Found: false}})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	if err = <-requestDone; err != nil {
		t.Fatal(err)
	}
	if !scope.applyRenew(BindingsRenewResult{BindingID: r.BindingID, ExpiresAt: r.ExpiresAt, RemainingBudgets: HostRPCRemainingBudgets{TimeoutMS: 10000}}, time.Now(), 1000) {
		t.Fatal("second metadata update")
	}
	scope.mu.Lock()
	if scope.leaseBudgetEnd.After(budget) || scope.leaseBudgets.Bytes == nil || *scope.leaseBudgets.Bytes != 0 {
		t.Fatal("remaining budget reset")
	}
	scope.terminal = true
	before := scope.leaseEnd
	scope.mu.Unlock()
	if scope.applyRenew(r, time.Now(), 300000) {
		t.Fatal("terminal renewal applied")
	}
	scope.mu.Lock()
	if scope.leaseEnd != before {
		t.Fatal("late authority changed")
	}
	scope.mu.Unlock()
}
func TestRenewReceiptAnchorDoesNotStartOnNextCall(t *testing.T) {
	h, ctx, core, scope := clientFixture(t, mcpInitFixture(t))
	core.publish = func([]byte, func(error)) error { t.Fatal("expired receipt published"); return nil }
	r := BindingsRenewResult{BindingID: "binding-example", ExpiresAt: "2099-01-01T00:00:00Z", RemainingBudgets: HostRPCRemainingBudgets{TimeoutMS: 100}}
	if !scope.applyRenew(r, time.Now().Add(-time.Second), 2000) {
		t.Fatal("metadata")
	}
	_, err := h.StorageGet(ctx, StorageGetArgs{"g-StorageGet", "k"})
	var deadline *DeadlineExceededError
	if !errors.As(err, &deadline) {
		t.Fatal("relative budget was restarted")
	}
}
func TestRenewDoesNotExtendInFlightCallOrCaller(t *testing.T) {
	h, ctx, core, scope := clientFixture(t, mcpInitFixture(t))
	frames := make(chan *RPCRequest, 4)
	core.publish = func(frame []byte, done func(error)) error {
		req, _ := decodeEnvelope(frame)
		frames <- req
		done(nil)
		return nil
	}
	caller, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	call, err := h.MCPCallTool(caller, MCPCallToolArgs{GrantID: "g-MCPCallTool", ServerID: "example.server", ToolName: "lookup", ToolBinding: "opaque", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	target := <-frames
	before, _ := call.pending.ctx.Deadline()
	parentBefore, _ := scope.ctx.Deadline()
	done := make(chan error, 1)
	go func() { _, err := h.BindingsRenew(ctx, BindingsRenewArgs{"g-storage", 300000}); done <- err }()
	req := <-frames
	reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": BindingsRenewResult{BindingID: "binding-example", ExpiresAt: "2099-01-01T00:00:00Z", RemainingBudgets: HostRPCRemainingBudgets{TimeoutMS: 10000}}})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	after, _ := call.pending.ctx.Deadline()
	parentAfter, _ := scope.ctx.Deadline()
	if before != after || parentBefore != parentAfter {
		t.Fatal("renew extended existing deadline")
	}
	reply, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": target.ID, "result": map[string]any{"content": []any{}, "is_error": false}})
	if err = core.reply(reply); err != nil {
		t.Fatal(err)
	}
	if _, err = call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestMCPHandlePublishedCancellationPreservesUnknownOutcome(t *testing.T) {
	h, ctx, core, _ := clientFixture(t, mcpInitFixture(t))
	core.publishCall = func(_ context.Context, _ []byte, done func(error), start func(), _ func() bool, _ func() ([]byte, error)) error {
		start()
		done(nil)
		return nil
	}
	core.publishControl = func(_ []byte, done func(error)) error { done(nil); return nil }
	caller, cancel := context.WithCancelCause(ctx)
	call, err := h.MCPCallTool(caller, MCPCallToolArgs{GrantID: "g-MCPCallTool", ServerID: "example.server", ToolName: "effect", ToolBinding: "host-issued", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	cancel(&TransportCancelledError{Reason: CallerCancelled})
	_, err = call.Wait(context.Background())
	var transport *RPCTransportError
	if !errors.As(err, &transport) || transport.Failure.Code != capability.UnknownOutcome || transport.Failure.EffectState != capability.Unknown {
		t.Fatalf("tool ambiguity %v", err)
	}
}
