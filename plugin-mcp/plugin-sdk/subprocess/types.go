package subprocess

import (
	"encoding/json"

	plugin "github.com/hollis-labs/plugin-sdk"
)

// --- Init handshake ---

// InitParams is sent by the host during plugin/init.
type InitParams struct {
	PluginDir string            `json:"plugin_dir"`
	Config    map[string]string `json:"config"`    // resolved config values
	HostInfo  HostInfo          `json:"host_info"` // host capabilities
}

// HostInfo describes the host environment to the plugin.
type HostInfo struct {
	Version  string `json:"version"`  // host version (e.g. nanite version)
	Protocol int    `json:"protocol"` // protocol version (see ProtocolVersion)
}

// InitResult is returned by the plugin in response to plugin/init.
type InitResult struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Protocol    int    `json:"protocol"` // protocol version the plugin supports
}

// --- Load registration manifest ---

// LoadParams is sent by the host during plugin/load (currently empty, reserved).
type LoadParams struct{}

// LoadResult is the registration manifest returned by the plugin during
// plugin/load. The host translates each section into the corresponding
// Register* calls.
//
// Some fields (Commands, Slots, Components, Keybindings) carry wire
// structs that mirror host-specific Go types. The host's public plugin
// package is responsible for translating these into its canonical types.
// Keeping them in the SDK keeps LoadResult a single decodable unit; the
// only cost is a few extra field definitions no non-Nanite host would
// populate.
type LoadResult struct {
	Dependencies       []string                `json:"dependencies,omitempty"`
	Commands           []CommandRegistration   `json:"commands,omitempty"`
	Slots              []UISlotEntry           `json:"slots,omitempty"`
	Components         []ComponentRegistration `json:"components,omitempty"`
	Keybindings        []KeybindingDef         `json:"keybindings,omitempty"`
	ConfigSchema       []plugin.ConfigFieldDef `json:"config_schema,omitempty"`
	EventSubscriptions []string                `json:"event_subscriptions,omitempty"`
	CRUDResources      []string                `json:"crud_resources,omitempty"` // resource type names for CRUD
}

// CommandRegistration is the wire representation of a slash command.
// The Handler field on the host-side SlashCommandDef cannot be
// serialized, so the host creates a proxy handler that calls
// command/execute over JSON-RPC when the command is invoked.
type CommandRegistration struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    string       `json:"category"`
	Args        []CommandArg `json:"args,omitempty"`
	Permission  string       `json:"required_permission,omitempty"`
}

// ComponentRegistration is the wire representation of a UIComponent.
// The Handler field is omitted — subprocess plugins serve UI via the
// frontend ESM loader, not via server-side handlers.
type ComponentRegistration struct {
	ID          string                 `json:"id"`
	Type        plugin.UIComponentType `json:"type"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Props       map[string]interface{} `json:"props,omitempty"`
}

// UISlotEntry is the wire representation of a UI slot registration.
// Slot names are host-defined strings (Nanite uses values like
// "nav-rail", "settings-tab"; see nanite/pkg/plugin for the typed
// constants).
type UISlotEntry struct {
	ID        string                 `json:"id"`
	PluginID  string                 `json:"plugin_id"`
	Slot      string                 `json:"slot"`
	Label     string                 `json:"label"`
	Icon      string                 `json:"icon,omitempty"`
	Priority  int                    `json:"priority,omitempty"`
	Component string                 `json:"component,omitempty"`
	Action    string                 `json:"action,omitempty"`
	Props     map[string]interface{} `json:"props,omitempty"`
}

// KeybindingDef is the wire representation of a keyboard shortcut
// registration. The Key field uses the binding format "mod+shift+k"
// where "mod" maps to Cmd on macOS and Ctrl elsewhere.
type KeybindingDef struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Action      string `json:"action"`
	ActionValue string `json:"action_value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// CommandArg is the wire representation of a slash command argument,
// used by the host frontend to render autocomplete hints and validate
// input.
type CommandArg struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Type        string   `json:"type,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// --- Runtime request/response types ---

// CommandExecParams is sent to the plugin for command/execute.
type CommandExecParams struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Args      string `json:"args"`
}

// CommandExecResult is returned by the plugin for command/execute.
type CommandExecResult struct {
	Action  string `json:"action"` // "message", "noop", "error"
	Content string `json:"content,omitempty"`
}

// EventHandleParams is sent to the plugin for event/handle.
type EventHandleParams struct {
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	Data      map[string]interface{} `json:"data"`
	SessionID string                 `json:"session_id,omitempty"`
	PreHook   bool                   `json:"pre_hook"` // true if host expects cancel/allow response
}

// EventHandleResult is returned by the plugin for event/handle.
type EventHandleResult struct {
	Cancel bool   `json:"cancel,omitempty"` // true to cancel a pre-hook action
	Reason string `json:"reason,omitempty"`
}

// CRUDParams is sent for all crud/* methods.
type CRUDParams struct {
	ResourceType string                 `json:"resource_type"`
	ID           string                 `json:"id,omitempty"`      // for read/update/delete
	Data         map[string]interface{} `json:"data,omitempty"`    // for create/update
	Filters      map[string]interface{} `json:"filters,omitempty"` // for list
}

// CRUDResult is returned for crud/create, crud/read, crud/update.
type CRUDResult struct {
	Data json.RawMessage `json:"data"`
}

// CRUDListResult is returned for crud/list.
type CRUDListResult struct {
	Items []json.RawMessage `json:"items"`
}

// HealthResult is returned by plugin/health.
type HealthResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}
