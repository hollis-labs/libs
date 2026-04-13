// Package plugin defines the universal Plugin SDK surface that both the host
// application and plugins import. It has zero dependencies on any host-specific
// code path; host-specific extensions live in the host's own public package
// (for Nanite, that is github.com/hollis-labs/nanite/pkg/plugin).
//
// The types here were extracted from github.com/hollis-labs/go-plugin during
// the Nanite plugin system overhaul (Phase 2 Track C, 2026-04-13).
package plugin

import (
	"context"
	"net/http"
	"time"
)

// Plugin is the minimum contract a plugin must implement. Optional capability
// interfaces (CRUDHandler, EventHook, Installable, Uninstallable, Connector)
// are detected via type assertion at registration time.
type Plugin interface {
	// ID returns a unique identifier for this plugin.
	ID() string

	// Name returns the human-readable name of the plugin.
	Name() string

	// Version returns the plugin version.
	Version() string

	// Description returns a brief description of the plugin's purpose.
	Description() string

	// Dependencies returns a list of required plugin IDs this plugin depends on.
	Dependencies() []string

	// Load is called when the plugin is loaded into the host.
	Load(host Host) error

	// Unload is called when the plugin is being removed.
	Unload() error

	// Status returns the current status of the plugin.
	Status() PluginStatus
}

// PluginStatus represents the current state of a plugin.
type PluginStatus struct {
	Loaded    bool      `json:"loaded"`
	Enabled   bool      `json:"enabled"`
	LoadedAt  time.Time `json:"loaded_at"`
	LastError string    `json:"last_error,omitempty"`
}

// Host provides the runtime environment and services available to plugins.
// This is the base Host contract — host applications may extend it with
// additional registration methods in their own public package (e.g., Nanite
// adds RegisterCommand / RegisterSlot / RegisterKeybinding in
// github.com/hollis-labs/nanite/pkg/plugin).
type Host interface {
	// GetPlugin retrieves another loaded plugin by ID.
	GetPlugin(id string) (Plugin, bool)

	// RegisterCRUDHandler registers a CRUD handler for a resource type.
	RegisterCRUDHandler(resourceType string, handler CRUDHandler) error

	// RegisterEventHook registers an event hook for specific event types.
	RegisterEventHook(eventTypes []string, hook EventHook) error

	// RegisterUIComponent registers UI components for the frontend.
	RegisterUIComponent(component UIComponent) error

	// GetService provides access to core services (MCP clients, databases, etc.).
	GetService(name string) (interface{}, error)

	// GetConfig returns a plugin configuration value.
	// Resolution order: env var -> DB setting -> config file -> default.
	// Returns an error if the key is required and not found.
	GetConfig(key string) (string, error)

	// SetConfig persists a configuration value for the calling plugin.
	SetConfig(key string, value string) error

	// RegisterConfigSchema registers the config field definitions for the
	// calling plugin. The frontend reads this schema to render settings
	// forms automatically.
	RegisterConfigSchema(fields []ConfigFieldDef) error

	// RegisterConnector registers a named outbound connector (webhook, email, etc.).
	RegisterConnector(name string, connector Connector) error

	// RegisterProvider registers a runtime LLM provider.
	RegisterProvider(name string, provider interface{}) error

	// RegisterCLIAdapter registers a runtime CLI adapter for PTY/subprocess bridges.
	RegisterCLIAdapter(name string, adapter interface{}) error

	// Logger provides a logger instance for the plugin.
	Logger() Logger

	// Context returns the plugin's execution context.
	Context() context.Context
}

// CRUDHandler defines CRUD operations for plugin-provided resources.
type CRUDHandler interface {
	Create(ctx context.Context, resource interface{}) (interface{}, error)
	Read(ctx context.Context, id string) (interface{}, error)
	Update(ctx context.Context, id string, resource interface{}) (interface{}, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filters map[string]interface{}) ([]interface{}, error)
}

// EventHook processes events from the host application.
//
// Implementations MUST report the ID of the plugin that owns them via
// PluginID so the host can cleanly unregister hooks during unload. This is
// the post-Track-C EventHook contract; pre-audit hooks that did not carry
// a PluginID were a source of leak bugs.
type EventHook interface {
	// Handle processes an event. Returning ErrCancelled from a pre-hook
	// event (e.g., message.sending) cancels the pending action.
	Handle(ctx context.Context, event Event) error

	// EventTypes returns the list of event types this hook handles.
	EventTypes() []string

	// PluginID returns the ID of the owning plugin. The host uses this to
	// remove all hooks belonging to a plugin during unload.
	PluginID() string
}

// Event represents an event in the system.
type Event struct {
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data"`
	SessionID string                 `json:"session_id,omitempty"`
}

// UIComponent represents a UI component that can be rendered in the frontend.
type UIComponent struct {
	ID          string                 `json:"id"`
	Type        UIComponentType        `json:"type"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Props       map[string]interface{} `json:"props,omitempty"`
	// Handler is used for server-side rendering (compiled-in plugins only).
	// Subprocess plugins cannot carry an http.Handler across the wire and
	// must leave this nil.
	Handler http.Handler `json:"-"`
}

// UIComponentType defines the type of UI component.
type UIComponentType string

const (
	UIComponentTypeWidget   UIComponentType = "widget"
	UIComponentTypeEnvelope UIComponentType = "envelope"
	UIComponentTypeAction   UIComponentType = "action"
	UIComponentTypeWorkflow UIComponentType = "workflow"
	UIComponentTypeView     UIComponentType = "view"
)

// Installable is an optional interface plugins implement to run setup logic
// during first-time installation. If not implemented, Load handles all setup.
type Installable interface {
	Install(host Host) error
}

// Uninstallable is an optional interface plugins implement to clean up
// artifacts during uninstallation. User-created data is preserved.
type Uninstallable interface {
	Uninstall(host Host) error
}

// Connector is the interface for outbound integrations (email, webhook, Slack, etc.).
type Connector interface {
	Name() string
	Send(ctx context.Context, payload map[string]interface{}) error
	Health(ctx context.Context) error
}

// ConfigFieldDef defines a single configuration field for frontend rendering.
type ConfigFieldDef struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"` // "string", "bool", "int", "select", "secret"
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Options     []string `json:"options,omitempty"`   // for "select" type
	Component   string   `json:"component,omitempty"` // custom React component (requires developer_mode)
}
