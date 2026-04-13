// Package subprocess carries the JSON-RPC 2.0 wire protocol used for
// host-to-plugin communication over stdio. These types are consumed by
// both the host (for encoding requests / decoding responses) and by the
// plugin side of the SDK (server.go / subprocess.Serve).
//
// Communication is host-initiated: the host sends JSON-RPC requests on
// the subprocess's stdin, and reads responses from its stdout. The
// subprocess does not initiate requests — all registrations are
// declarative via the plugin/load response.
package subprocess

import "encoding/json"

// --- JSON-RPC wire types ---

// RPCRequest is a JSON-RPC 2.0 request sent from host to plugin.
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"` // 0 for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCResponse is a JSON-RPC 2.0 response from plugin to host.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface.
func (e *RPCError) Error() string { return e.Message }

// --- Method constants ---

const (
	// Lifecycle methods.
	MethodInit   = "plugin/init"
	MethodLoad   = "plugin/load"
	MethodUnload = "plugin/unload"
	MethodHealth = "plugin/health"

	// Runtime methods (host -> plugin).
	MethodCommandExecute = "command/execute"
	MethodEventHandle    = "event/handle"
	MethodCRUDCreate     = "crud/create"
	MethodCRUDRead       = "crud/read"
	MethodCRUDUpdate     = "crud/update"
	MethodCRUDDelete     = "crud/delete"
	MethodCRUDList       = "crud/list"
)

// --- Standard JSON-RPC error codes ---

const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603

	// Application-level error codes (plugin-specific).
	ErrCodeNotFound   = -32000
	ErrCodeConflict   = -32001
	ErrCodeValidation = -32002
	ErrCodeCancelled  = -32003 // pre-hook cancellation
)

// ProtocolVersion is the current wire-protocol version negotiated during
// the plugin/init handshake. Host and plugin must agree exactly.
const ProtocolVersion = 1
