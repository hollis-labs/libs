package subprocess

import (
	"context"

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
}

// CommandResult is returned by a plugin's Command handler.
type CommandResult struct {
	Action    string                // "message", "noop", "error"
	Content   string                // message body (for "message" action)
	Envelopes []plugin.EnvelopeOut  // optional envelopes to emit alongside the message
}

// EventRequest is passed to a plugin's EventHandle handler.
type EventRequest struct {
	Type      string
	Source    string
	Data      map[string]interface{}
	SessionID string
	PreHook   bool // true if the host expects a cancel/allow response
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
// on-disk version differs from the installed version.
type Migrator interface {
	Migrate(ctx context.Context, from, to string) error
}
