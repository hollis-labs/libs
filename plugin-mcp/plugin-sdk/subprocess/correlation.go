package subprocess

import (
	"encoding/json"
	"errors"
	"github.com/hollis-labs/plugin-sdk/internal/strictjson"
	"sync"
)

var errConnectionClosed = errors.New("subprocess: connection closed")
var errCorrelation = errors.New("subprocess: invalid correlation")

const coreCapacity = 256

// Internal foundation; production reverse activation is deliberately absent.
// Fixture constructors use the same engine with explicit directional state.
type correlation struct {
	mu          sync.Mutex
	publishMu   sync.Mutex
	directional bool
	incoming    map[RPCID]struct{}
	pending     map[int64]*pendingCall
	next, high  int64
	closed      error
	publish     func([]byte, func(error)) error
	encode      func(any) ([]byte, error)
}
type pendingCall struct {
	resultDTO string
	done      chan callResult
}
type callResult struct {
	result json.RawMessage
	err    error
}

func newCorrelation(directional bool) *correlation {
	return &correlation{directional: directional, incoming: make(map[RPCID]struct{}), pending: make(map[int64]*pendingCall)}
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
		p.done <- callResult{err: err}
	}
	c.incoming = make(map[RPCID]struct{})
}
func (c *correlation) fail(id int64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.pending[id]; p != nil {
		delete(c.pending, id)
		p.done <- callResult{err: err}
	}
}

// register and publish are serialized together; a synchronous peer reply can
// already find the pending entry. A consumed ID is never reused on failure.
func (c *correlation) call(method string, params json.RawMessage) (<-chan callResult, error) {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	pair, ok := hostMethods[method]
	if !ok {
		return nil, errCorrelation
	}
	if err := ValidateHostRPCDTO(pair[0], params); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if !c.directional || c.closed != nil || len(c.pending) >= coreCapacity || c.next == maxRPCInteger {
		c.mu.Unlock()
		return nil, errCorrelation
	}
	c.next++
	id := c.next
	p := &pendingCall{resultDTO: pair[1], done: make(chan callResult, 1)}
	c.pending[id] = p
	c.mu.Unlock()
	// A plain struct bypasses RPCRequest.MarshalJSON's unbounded staging.
	value := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", id, method, params}
	frame, err := c.encode(value)
	if err == nil {
		err = c.publish(frame, func(err error) {
			if err != nil {
				c.fail(id, err)
			}
		})
	}
	if err != nil {
		c.fail(id, err)
	}
	return p.done, nil
}

var hostMethods = map[string][2]string{
	"host/storage/get": {"StorageGetParams", "StorageGetResult"}, "host/storage/put": {"StoragePutParams", "StoragePutResult"}, "host/storage/delete": {"StorageDeleteParams", "StorageDeleteResult"}, "host/secrets/get": {"SecretsGetParams", "SecretsGetResult"}, "host/egress/request": {"EgressRequestParams", "EgressRequestResult"}, "host/events/publish": {"EventsPublishParams", "EventsPublishResult"}, "host/log": {"LogParams", "LogResult"}, "host/readonly/query": {"ReadonlyQueryParams", "ReadonlyQueryResult"}, "host/mcp/list_tools": {"MCPListToolsParams", "MCPListToolsResult"}, "host/mcp/call_tool": {"MCPCallToolParams", "MCPCallToolResult"}, "host/mcp/cancel_call": {"MCPCancelCallParams", "MCPCancelCallResult"}, "host/bindings/renew": {"BindingsRenewParams", "BindingsRenewResult"},
}

func (c *correlation) reply(raw []byte) error {
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
	var result callResult
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[n] == p {
		delete(c.pending, n)
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
	return strictjson.ValidatePortable(raw)
}
