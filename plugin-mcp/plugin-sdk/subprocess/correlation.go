package subprocess

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
	"sync"
	"sync/atomic"
	"time"
)

var errConnectionClosed = errors.New("subprocess: connection closed")
var errCorrelation = errors.New("subprocess: invalid correlation")

const coreCapacity = 256

// Internal engine. Negotiation owns production activation; fixtures can select
// the same directional foundation explicitly without claiming negotiated evidence.
type correlation struct {
	mu              sync.Mutex
	publishMu       sync.Mutex
	directional     bool
	provisional     bool
	incoming        map[RPCID]struct{}
	pending         map[int64]*pendingCall
	next, high      int64
	closed          error
	publish         func([]byte, func(error)) error
	encode          func(any) ([]byte, error)
	methodTimeoutMS map[string]uint32
	reversePermits  chan struct{}
	publishCall     func(context.Context, []byte, func(error), func(), func() bool, func() ([]byte, error)) error
	publishControl  func([]byte, func(error)) error
}
type pendingCall struct {
	resultDTO     string
	done          chan callResult
	cancel        context.CancelFunc
	possible      atomic.Bool
	settled       atomic.Bool
	releasePermit func()
	method        string
	descendant    bool
	ctx           context.Context
}
type callResult struct {
	result     json.RawMessage
	err        error
	receivedAt time.Time
}

func newCorrelation(directional bool) *correlation {
	return &correlation{directional: directional, incoming: make(map[RPCID]struct{}), pending: make(map[int64]*pendingCall), reversePermits: make(chan struct{}, ReverseHandlerSlots)}
}
func (c *correlation) admit(id RPCID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed != nil {
		return c.closed
	}
	if id == (RPCID{}) {
		return nil
	}
	if _, ok := c.incoming[id]; ok {
		return errCorrelation
	}
	if len(c.incoming) >= coreCapacity {
		return errCorrelation
	}
	if c.directional {
		n, ok := id.Integer()
		if !ok || n <= c.high || !id.positiveInteger() {
			return errCorrelation
		}
		c.high = n
	}
	c.incoming[id] = struct{}{}
	return nil
}
func (c *correlation) release(id RPCID) { c.mu.Lock(); delete(c.incoming, id); c.mu.Unlock() }
func (c *correlation) close(err error) {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	if err == nil {
		err = errConnectionClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed != nil {
		return
	}
	c.closed = err
	for id, p := range c.pending {
		delete(c.pending, id)
		p.settled.Store(true)
		if p.releasePermit != nil {
			p.releasePermit()
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.done <- callResult{err: callTransportError(id, p, err)}
	}
	c.incoming = make(map[RPCID]struct{})
}
func (c *correlation) fail(id int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pending[id]; p != nil {
		delete(c.pending, id)
		p.settled.Store(true)
		if p.releasePermit != nil {
			p.releasePermit()
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.done <- callResult{err: callTransportError(id, p, err)}
	}
}

// register and publish are serialized together; a synchronous peer reply can
// already find the pending entry. A consumed ID is never reused on failure.
func (c *correlation) call(method string, params json.RawMessage) (<-chan callResult, error) {
	return c.callContext(context.Background(), method, params)
}

// callContext is an internal seam for typed helpers. Offers supply ceilings;
// no process-wide method timeout or host authority ledger is invented here.
func (c *correlation) callContext(parent context.Context, method string, params json.RawMessage) (<-chan callResult, error) {
	return c.callTracked(parent, method, params, nil)
}

func (c *correlation) callTracked(parent context.Context, method string, params json.RawMessage, registered func(int64, *pendingCall)) (<-chan callResult, error) {
	started := time.Now()
	pair, ok := hostMethods[method]
	if !ok {
		return nil, errCorrelation
	}
	select {
	case c.reversePermits <- struct{}{}:
	default:
		return nil, &capability.Error{Code: capability.RateLimited, EffectState: capability.NotStarted}
	}
	owned := true
	defer func() {
		if owned {
			<-c.reversePermits
		}
	}()
	c.mu.Lock()
	ceiling := c.methodTimeoutMS[method]
	c.mu.Unlock()
	if ceiling == 0 {
		return nil, errors.New("subprocess: method has no offered timeout")
	}
	if err := ValidateHostRPCDTO(pair[0], params); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(params, &fields)
	var reverse ReverseContext
	_ = json.Unmarshal(fields["context"], &reverse)
	ms := reverse.TimeoutMS
	if ceiling < ms {
		ms = ceiling
	}
	if scope := scopeFromContext(parent); scope != nil {
		id, valid := scope.id.Integer()
		if !valid || id <= 0 || reverse.ParentCall.RequestOwner != HostRPCOwnerHost || reverse.ParentCall.ID != uint64(id) {
			return nil, errors.New("subprocess: parent_invalid")
		}
		if scope.binding != nil && *scope.binding != reverse.BindingID {
			return nil, errors.New("subprocess: parent_invalid")
		}
	}
	ctx, cancel := context.WithDeadlineCause(parent, started.Add(time.Duration(ms)*time.Millisecond), &DeadlineExceededError{})
	if cause := requestContextFailure(ctx); cause != nil {
		cancel()
		return nil, cause
	}
	// The parent may have a tighter deadline; publish the remaining clipped request.
	if end, ok := ctx.Deadline(); ok {
		remaining := time.Until(end) / time.Millisecond
		if remaining < 1 {
			cancel()
			return nil, &DeadlineExceededError{}
		}
		reverse.TimeoutMS = uint32(remaining)
	}
	contextRaw, _ := json.Marshal(reverse)
	fields["context"] = contextRaw
	clipped, err := marshalBounded(fields, MaxHostRPCDTOBytes)
	if err != nil {
		cancel()
		return nil, err
	}
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	if cause := requestContextFailure(ctx); cause != nil {
		cancel()
		return nil, cause
	}
	c.mu.Lock()
	if !(c.directional || c.provisional) || c.closed != nil || c.next == maxRPCInteger {
		c.mu.Unlock()
		cancel()
		return nil, errCorrelation
	}
	if len(c.pending) >= cap(c.reversePermits) {
		c.mu.Unlock()
		cancel()
		return nil, &capability.Error{Code: capability.RateLimited, EffectState: capability.NotStarted}
	}
	c.next++
	id := c.next
	p := &pendingCall{resultDTO: pair[1], done: make(chan callResult, 1), cancel: cancel, method: method, descendant: scopeFromContext(parent) != nil, ctx: ctx}
	var permitOnce sync.Once
	p.releasePermit = func() { permitOnce.Do(func() { <-c.reversePermits }) }
	owned = false
	c.pending[id] = p
	if registered != nil {
		registered(id, p)
	}
	c.mu.Unlock()
	value := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", id, method, clipped}
	frame, err := c.encode(value)
	if cause := requestContextFailure(ctx); cause != nil {
		c.expire(id, p, cause)
		return p.done, nil
	}
	if err == nil {
		receipt := func(err error) {
			if err != nil {
				if cause := requestContextFailure(ctx); cause != nil {
					c.expire(id, p, cause)
				} else {
					c.fail(id, err)
				}
			}
		}
		if c.publishCall != nil {
			prepare := func() ([]byte, error) {
				if cause := requestContextFailure(ctx); cause != nil {
					return nil, cause
				}
				end, _ := ctx.Deadline()
				remaining := time.Until(end) / time.Millisecond
				if remaining < 1 {
					return nil, &DeadlineExceededError{}
				}
				reverse.TimeoutMS = uint32(remaining)
				contextRaw, err := json.Marshal(reverse)
				if err != nil {
					return nil, err
				}
				fields["context"] = contextRaw
				value.Params, err = marshalBounded(fields, MaxHostRPCDTOBytes)
				if err != nil {
					return nil, err
				}
				return c.encode(value)
			}
			err = c.publishCall(ctx, frame, receipt, func() { p.possible.Store(true) }, func() bool { return p.settled.Load() }, prepare)
		} else {
			p.possible.Store(true)
			err = c.publish(frame, receipt)
		}
	}
	if err != nil {
		if cause := requestContextFailure(ctx); cause != nil {
			c.expire(id, p, cause)
		} else {
			c.fail(id, err)
		}
	}
	context.AfterFunc(ctx, func() { c.expire(id, p, context.Cause(ctx)) })
	return p.done, nil
}
func mutationMethod(method string) bool {
	switch method {
	case "host/storage/put", "host/storage/delete", "host/events/publish", "host/egress/request", "host/mcp/call_tool", "host/mcp/cancel_call", "host/bindings/renew":
		return true
	}
	return false
}
func (c *correlation) expire(id int64, p *pendingCall, cause error) {
	c.mu.Lock()
	if c.pending[id] != p {
		c.mu.Unlock()
		return
	}
	delete(c.pending, id)
	if p.cancel != nil {
		p.cancel()
	}
	if p.releasePermit != nil {
		p.releasePermit()
	}
	c.mu.Unlock()
	code := capability.Cancelled
	var deadline *DeadlineExceededError
	if errors.As(cause, &deadline) || errors.Is(cause, context.DeadlineExceeded) {
		code = capability.DeadlineExceeded
	}
	state := capability.NotStarted
	if p.possible.Load() {
		state = capability.Unknown
		if mutationMethod(p.method) {
			code = capability.UnknownOutcome
		}
	}
	p.done <- callResult{err: &RPCTransportError{Failure: capability.Error{Code: code, RequestID: capability.RequestID(id), EffectState: state}, cause: cause}}
	if p.possible.Load() && c.publishControl != nil {
		reason := CallerCancelled
		if p.descendant {
			reason = ParentCancelled
		}
		if errors.As(cause, &deadline) || errors.Is(cause, context.DeadlineExceeded) {
			reason = DeadlineExpired
		}
		var cancelled *TransportCancelledError
		if errors.As(cause, &cancelled) {
			reason = cancelled.Reason
			if p.descendant {
				reason = ParentCancelled
			}
		}
		value := struct {
			JSONRPC string       `json:"jsonrpc"`
			Method  string       `json:"method"`
			Params  CancelParams `json:"params"`
		}{"2.0", "rpc/cancel", CancelParams{HostRPCOwnerPlugin, NumberID(id), reason}}
		raw, err := c.encode(value)
		if err == nil {
			err = c.publishControl(raw, func(err error) {
				if err != nil {
					c.close(err)
				}
			})
		}
		if err != nil {
			c.close(err)
		}
	}
}

var hostMethods = map[string][2]string{
	"host/storage/get": {"StorageGetParams", "StorageGetResult"}, "host/storage/put": {"StoragePutParams", "StoragePutResult"}, "host/storage/delete": {"StorageDeleteParams", "StorageDeleteResult"}, "host/secrets/get": {"SecretsGetParams", "SecretsGetResult"}, "host/egress/request": {"EgressRequestParams", "EgressRequestResult"}, "host/events/publish": {"EventsPublishParams", "EventsPublishResult"}, "host/log": {"LogParams", "LogResult"}, "host/readonly/query": {"ReadonlyQueryParams", "ReadonlyQueryResult"}, "host/mcp/list_tools": {"MCPListToolsParams", "MCPListToolsResult"}, "host/mcp/call_tool": {"MCPCallToolParams", "MCPCallToolResult"}, "host/mcp/cancel_call": {"MCPCancelCallParams", "MCPCancelCallResult"}, "host/bindings/renew": {"BindingsRenewParams", "BindingsRenewResult"},
}

func (c *correlation) reply(raw []byte) error {
	receivedAt := time.Now()
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errCorrelation
	}
	if strictjson.ValidatePortable(raw) != nil {
		return errCorrelation
	}
	for key := range fields {
		if key != "jsonrpc" && key != "id" && key != "result" && key != "error" {
			return errCorrelation
		}
	}
	if body, ok := fields["error"]; ok {
		if err := validateReplyError(body); err != nil {
			return errCorrelation
		}
		var fault RPCError
		_ = json.Unmarshal(body, &fault)
		if fault.Code == -32010 && ValidateHostRPCDTO("ApplicationErrorResponse", raw) != nil {
			return errCorrelation
		}
	}
	id, err := parseRPCID(fields["id"])
	if err != nil {
		return errCorrelation
	}
	n, ok := id.Integer()
	if !ok {
		return nil
	}
	c.mu.Lock()
	p := c.pending[n]
	c.mu.Unlock()
	if p == nil {
		return nil
	}
	result := callResult{receivedAt: receivedAt}
	if body, ok := fields["result"]; ok {
		if err := ValidateHostRPCDTO(p.resultDTO, body); err != nil {
			return errCorrelation
		}
		result.result = body
	} else {
		if err := validateReplyError(fields["error"]); err != nil {
			return errCorrelation
		}
		var fault RPCError
		if json.Unmarshal(fields["error"], &fault) != nil {
			return errCorrelation
		}
		if fault.Code == -32010 {
			if ValidateHostRPCDTO("ApplicationErrorResponse", raw) != nil {
				return errCorrelation
			}
		}
		result.err = &fault
	}
	if p.ctx != nil {
		cause := context.Cause(p.ctx)
		if end, ok := p.ctx.Deadline(); ok && !time.Now().Before(end) {
			cause = &DeadlineExceededError{}
		}
		if cause != nil {
			c.expire(n, p, cause)
			return nil
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[n] == p {
		delete(c.pending, n)
		p.settled.Store(true)
		if p.releasePermit != nil {
			p.releasePermit()
		}
		if p.cancel != nil {
			p.cancel()
		}
		p.done <- result
	}
	return nil
}
func validateReplyError(raw []byte) error {
	// Closed, exact keys and duplicate/portable syntax are checked before decode.
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errCorrelation
	}
	for k := range fields {
		if k != "code" && k != "message" && k != "data" {
			return errCorrelation
		}
	}
	if data, ok := fields["data"]; ok {
		var f map[string]json.RawMessage
		var contract string
		if json.Unmarshal(data, &f) == nil && json.Unmarshal(f["contract"], &contract) == nil && contract == "plugin-rpc/2" {
			if ValidateRPCControlDTO("PluginRPCErrorData", data, false) != nil {
				return errCorrelation
			}
		}
	}
	return strictjson.ValidatePortable(raw)
}

func callTransportError(id int64, p *pendingCall, cause error) error {
	code, state := capability.TargetUnavailable, capability.NotStarted
	if errors.Is(cause, errPublicationFull) {
		code = capability.RateLimited
	}
	var tooLarge *FrameTooLargeError
	if errors.As(cause, &tooLarge) {
		code = capability.BudgetExceeded
	}
	if p.possible.Load() {
		state = capability.Unknown
		if mutationMethod(p.method) {
			code = capability.UnknownOutcome
		}
	}
	return &RPCTransportError{Failure: capability.Error{Code: code, RequestID: capability.RequestID(id), EffectState: state}, cause: cause}
}
