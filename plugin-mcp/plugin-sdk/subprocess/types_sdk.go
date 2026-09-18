package subprocess

import (
	"context"
	"encoding/json"

	plugin "github.com/hollis-labs/plugin-sdk"
)

// --- SDK-level Go types exposed to plugin authors ---
//
// These are the friendly types that plugin code sees. Serve translates
// between these and the wire types in types.go before writing to
// stdout.

// CommandRequest is passed to a plugin's Command handler.
type CommandRequest struct {
	Name      string
	SessionID string
	Args      string
	// Identity is the opaque, host-verified identity value from the
	// wire request, if any. See IdentityAware.
	Identity json.RawMessage
}

// CommandResult is returned by a plugin's Command handler.
type CommandResult struct {
	Action    string               // "message", "noop", "error"
	Content   string               // message body (for "message" action)
	Envelopes []plugin.EnvelopeOut // optional envelopes to emit alongside the message
}

// EventRequest is passed to a plugin's EventHandle handler.
type EventRequest struct {
	Type      string
	Source    string
	Data      map[string]interface{}
	SessionID string
	PreHook   bool // true if the host expects a cancel/allow response
	// Identity is the opaque, host-verified identity value from the
	// wire request, if any. See IdentityAware.
	Identity json.RawMessage
}

// EventResult is returned by a plugin's EventHandle handler.
type EventResult struct {
	Cancel    bool
	Reason    string
	Envelopes []plugin.EnvelopeOut
}

// HealthResult mirrors the wire HealthResult but is promoted to the
// SDK surface so plugin authors never import the wire package directly.
// Health check payload returned by a plugin's Health method.
type HealthStatus struct {
	OK      bool
	Message string
}

// --- Plugin-side optional capability interfaces ---
//
// A plugin that implements one of these interfaces signals to Serve
// that it wants to receive those method calls. Serve detects
// implementations via type assertion at startup and reports unsupported
// methods as JSON-RPC method-not-found errors.

// Plugin is the minimum contract a subprocess plugin must satisfy.
// Metadata (ID/Name/Version/Description/Protocol) is returned by Init
// rather than by separate getter methods to keep the subprocess plugin
// shape distinct from the in-process plugin.Plugin interface.
type Plugin interface {
	Init(ctx context.Context, params InitParams) (InitResult, error)
	Load(ctx context.Context) (LoadResult, error)
	Unload(ctx context.Context) error
}

// CommandHandler is implemented by plugins that handle slash commands.
type CommandHandler interface {
	Command(ctx context.Context, req CommandRequest) (CommandResult, error)
}

// EventHandler is implemented by plugins that subscribe to host events.
type EventHandler interface {
	EventHandle(ctx context.Context, req EventRequest) (EventResult, error)
}

// HealthChecker is implemented by plugins that expose a health endpoint
// for the host's periodic health check. If not implemented, the host's
// health probe treats the plugin as healthy-by-default.
type HealthChecker interface {
	Health(ctx context.Context) (HealthStatus, error)
}

// CRUDHandler is implemented by plugins that own a resource type with
// Create/Read/Update/Delete/List semantics.
type CRUDHandler interface {
	Create(ctx context.Context, resourceType string, data map[string]interface{}) (map[string]interface{}, error)
	Read(ctx context.Context, resourceType, id string) (map[string]interface{}, error)
	Update(ctx context.Context, resourceType, id string, data map[string]interface{}) (map[string]interface{}, error)
	Delete(ctx context.Context, resourceType, id string) error
	List(ctx context.Context, resourceType string, filters map[string]interface{}) ([]map[string]interface{}, error)
}

// Migrator is implemented by plugins that need to run schema migrations
// on version upgrades. The host calls Migrate before Load when the
// on-disk version differs from the installed version. Returning a nil
// error is equivalent to a no-op migration.
type Migrator interface {
	Migrate(ctx context.Context, from, to string) error
}

// MCPHandler is implemented by plugins that expose MCP tools. The host
// dispatches mcp/call_tool requests here when a tool registered via
// plugin.yaml is invoked. Tool registration itself is declarative; the
// handler services runtime invocations only.
type MCPHandler interface {
	MCPCallTool(ctx context.Context, req MCPCallRequest) (MCPCallResult, error)
}

// HTTPHandler is implemented by plugins that service host-registered
// HTTP routes. The host dispatches http/handle requests here. Streaming
// is not supported on this path — emit SSE envelopes via command or
// event results instead.
type HTTPHandler interface {
	HTTPHandle(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// IdentityAware is implemented by plugins that want to be told the
// opaque identity value a host has already verified for the current
// caller. plugin-sdk never parses, validates, or picks a token scheme
// for this value — it is a courier, not an authority; verification
// happens entirely on the host side.
//
// Serve calls Identity once after a successful Init when
// InitParams.Identity is non-empty (the connection-level identity for
// this subprocess), and again before each Command/EventHandle/
// MCPCallTool/HTTPHandle dispatch whose own request carries a
// non-empty Identity — a single inprocess-mode subprocess can still
// serve calls on behalf of different verified callers over its
// lifetime, so a per-call Identity is not assumed to match Init's.
// Serve never calls Identity with an empty value: a plugin that
// implements IdentityAware but whose host never populates Identity
// sees no calls at all.
//
// A plugin that doesn't implement IdentityAware is completely
// unaffected — the Identity field remains directly readable off
// CommandRequest / EventRequest / MCPCallRequest / HTTPRequest for a
// plugin that would rather inspect it inline instead.
type IdentityAware interface {
	Identity(ctx context.Context, identity json.RawMessage)
}
