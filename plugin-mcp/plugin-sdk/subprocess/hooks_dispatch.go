package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Only package-local _test code assigns this fixture hook. No shipped option,
// environment variable, author interface or Init offer enables the profile.
var hookFixtureSetup func(*server)

func hookOperational(p HookHandleParams, status, code string) HookHandleResult {
	return HookHandleResult{InvocationID: p.InvocationID, Status: status, Error: &HookFailure{Code: code}}
}
func hookInvoke(ctx context.Context, h HookHandler, p HookHandleParams) (r HookHandleResult) {
	defer func() {
		if recover() != nil {
			r = hookOperational(p, "failed", "handler_panic")
		}
	}()
	r, err := h.HookHandle(ctx, p)
	if err != nil {
		code := "handler_error"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "deadline_exceeded"
		} else if errors.Is(err, context.Canceled) {
			code = "caller_cancelled"
		}
		return hookOperational(p, "failed", code)
	}
	if ValidateHookResultFor(p, r) != nil {
		return hookOperational(p, "failed", "invalid_output")
	}
	encoded, err := EncodeHookHandleResult(r)
	if err != nil {
		return hookOperational(p, "failed", "invalid_output")
	}
	copy, err := DecodeHookHandleResult(encoded)
	if err != nil {
		return hookOperational(p, "failed", "invalid_output")
	}
	return copy
}
func hookLease(ctx context.Context, p HookHandleParams, received time.Time) (context.Context, context.CancelFunc) {
	ms := p.Context.TimeoutMS
	if p.AggregateBudgetMS < ms {
		ms = p.AggregateBudgetMS
	}
	// The host's monotonic lease is authoritative; wall-clock deadline is carried
	// for diagnostics and cannot extend this relative lease.
	return context.WithDeadline(withForwardContext(ctx, &p.Context), received.Add(time.Duration(ms)*time.Millisecond))
}
func hookAwait(ctx context.Context, h HookHandler, p HookHandleParams) HookHandleResult {
	if ctx.Err() != nil {
		return hookContextFailure(ctx, p)
	}
	done := make(chan HookHandleResult, 1)
	go func() { done <- hookInvoke(ctx, h, p) }()
	select {
	case <-ctx.Done():
		return hookContextFailure(ctx, p)
	case r := <-done:
		if ctx.Err() != nil {
			return hookContextFailure(ctx, p)
		}
		return r
	}
}
func hookContextFailure(ctx context.Context, p HookHandleParams) HookHandleResult {
	code := "caller_cancelled"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = "deadline_exceeded"
	}
	return hookOperational(p, "failed", code)
}
func (s *server) dispatchHook(ctx context.Context, req RPCRequest) {
	received := time.Now()
	if !s.hooksFixtureEnabled {
		s.writeHookError(req.ID, ErrCodeMethodNotFound, "profile_unavailable", nil)
		return
	}
	h, ok := s.plugin.(HookHandler)
	if !ok {
		s.writeHookError(req.ID, ErrCodeMethodNotFound, "method_not_found", nil)
		return
	}
	notification := req.ID == (RPCID{})
	if !notification && !req.ID.positiveInteger() {
		s.writeHookError(req.ID, ErrCodeInvalidRequest, "invalid_request", nil)
		return
	}
	raw, err := paramsJSON(req.Params)
	if err != nil {
		s.writeHookError(req.ID, ErrCodeInvalidParams, "invalid_params", hookInvalid("params", "invalid params"))
		return
	}
	var items []HookHandleParams
	if req.Method == MethodHookHandle {
		var p HookHandleParams
		err = json.Unmarshal(raw, &p)
		if err == nil {
			err = ValidateHookRequest(p, notification)
		}
		items = []HookHandleParams{p}
	} else {
		var p HookHandleBatchParams
		err = json.Unmarshal(raw, &p)
		if err == nil {
			err = ValidateHookBatchRequest(p, notification)
		}
		items = p.Items
	}
	if err != nil {
		s.writeHookError(req.ID, ErrCodeInvalidParams, "invalid_params", hookInvalid("params", "invalid params"))
		return
	}
	// All item shapes are checked before any handler starts. Leases begin before
	// queueing earlier items; batch processing cannot grant later items fresh time.
	contexts := make([]context.Context, len(items))
	cancels := make([]context.CancelFunc, len(items))
	for i, p := range items {
		contexts[i], cancels[i] = hookLease(ctx, p, received)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	results := make([]HookHandleResult, len(items))
	for i, p := range items {
		if p.Scope.Incarnation != s.hooksIncarnation {
			results[i] = hookOperational(p, "unavailable", "stale_scope")
		} else {
			results[i] = hookAwait(contexts[i], h, p)
		}
		if notification && results[i].Status != "ok" {
			s.logger.Warn("hook notification failed", "status", results[i].Status)
		}
	}
	if notification {
		return
	}
	var payload []byte
	if req.Method == MethodHookHandle {
		payload, err = itemsResultJSON(results[0])
	} else {
		payload, err = itemsResultJSON(HookHandleBatchResult{Items: results})
	}
	if err != nil {
		s.writeError(req.ID, ErrCodeInternal, "invalid hook output")
		return
	}
	id, _ := req.ID.MarshalJSON()
	frame := append([]byte(`{"jsonrpc":"2.0","id":`), id...)
	frame = append(frame, []byte(`,"result":`)...)
	frame = append(frame, payload...)
	frame = append(frame, '}', '\n')
	s.writeFrame(frame)
}
func itemsResultJSON(v any) ([]byte, error) {
	switch r := v.(type) {
	case HookHandleResult:
		return r.MarshalJSON()
	case HookHandleBatchResult:
		return r.MarshalJSON()
	default:
		return nil, hookInvalid("result", "unknown hook result")
	}
}
