package subprocess

import (
	"encoding/json"
	"errors"
	"os"

	plugin "github.com/hollis-labs/libs/plugin-mcp/plugin-sdk"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

// ErrNoDataDir is returned by InitParams.ResolvedDataDir when the host
// did not provide a DataDir. Valid protocol-2 Init requires it; direct
// helper callers may still have an incomplete value.
var ErrNoDataDir = errors.New("subprocess: InitParams.DataDir not set by host")

// ResolvedDataDir returns the host-provided DataDir, or ErrNoDataDir
// if the host did not populate it. Plugins
// should treat an error here as fatal for any persistence path; there
// is no safe fallback for data.
func (p *InitParams) ResolvedDataDir() (string, error) {
	if p.DataDir == "" {
		return "", ErrNoDataDir
	}
	return p.DataDir, nil
}

// ResolvedCacheDir returns the host-provided CacheDir. If the host did
// not populate it, it falls back to
// os.TempDir(). The returned error is currently always nil; the
// signature returns error to allow future validation without breaking
// callers.
func (p *InitParams) ResolvedCacheDir() (string, error) {
	if p.CacheDir == "" {
		return os.TempDir(), nil
	}
	return p.CacheDir, nil
}

// --- Init handshake ---

// InitParams is sent by the host during plugin/init.
type InitParams struct {
	Context            *ForwardContext            `json:"context,omitempty"`
	PluginDir          string                     `json:"plugin_dir"`
	DataDir            string                     `json:"data_dir"`
	CacheDir           string                     `json:"cache_dir"`
	Config             map[string]string          `json:"config"`
	LogLevel           string                     `json:"log_level"`
	HostInfo           HostInfo                   `json:"host_info"`
	CapabilityContract int                        `json:"capability_contract"`
	Incarnation        capability.RuntimeIdentity `json:"incarnation"`
	Grants             capability.GrantSet        `json:"grants"`
	// Identity is an optional opaque verified courier, never incarnation identity
	// or an implicit initiating caller for subsequent requests.
	Identity     json.RawMessage `json:"identity,omitempty"`
	HostServices *HostServices   `json:"host_services,omitempty"`
	HooksProfile *HooksProfile   `json:"hooks_profile,omitempty"`
}

// HostInfo advertises an exact subprocess protocol version.
type HostInfo struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// InitResult acknowledges the base contract and any selected optional profiles.
// Serve owns acknowledgements: hooks requires an offer/handler, reverse an offer/opt-in.
type InitResult struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Version             string `json:"version"`
	Description         string `json:"description"`
	Protocol            int    `json:"protocol"`
	CapabilityContract  int    `json:"capability_contract"`
	ReverseRPCVersion   *int   `json:"reverse_rpc_version,omitempty"`
	HooksProfileVersion *int   `json:"hooks_profile_version,omitempty"`
}

// HooksProfile offers the independent hooks protocol. Serve requires HookHandler to acknowledge it.
type HooksProfile struct {
	HooksProfileVersion int `json:"hooks_profile_version"`
}

// HostServices is an availability offer, never an authorization grant.
type HostServices struct {
	ReverseRPCVersion int                        `json:"reverse_rpc_version"`
	Incarnation       capability.RuntimeIdentity `json:"incarnation"`
	Methods           []string                   `json:"methods"`
	Limits            HostServiceLimits          `json:"limits"`
}
type HostServiceLimits struct {
	HostToPluginInflight uint32            `json:"host_to_plugin_inflight"`
	PluginToHostInflight uint32            `json:"plugin_to_host_inflight"`
	HostGlobalInflight   uint32            `json:"host_global_inflight"`
	ControlSlots         uint32            `json:"control_slots"`
	MaxFrameBytes        uint32            `json:"max_frame_bytes"`
	MaxQueuedWriteBytes  uint32            `json:"max_queued_write_bytes"`
	WriteTimeoutMS       uint32            `json:"write_timeout_ms"`
	MaxDepth             uint32            `json:"max_depth"`
	MethodTimeoutMS      map[string]uint32 `json:"method_timeout_ms"`
}

// --- Load registration manifest ---

// LoadParams is sent by the host during plugin/load (currently empty, reserved).
type LoadParams struct {
	Context *ForwardContext `json:"context,omitempty"`
}

// LoadResult is the plugin's acknowledgement of plugin/load. As of
// v0.2.0, declarative registrations (commands, slots, components,
// keybindings, config schema, event subscriptions, CRUD resources) are
// yaml-authoritative — the host reads them from plugin.yaml and applies
// them directly. Plugins no longer return these fields at runtime.
//
// The only payload a plugin may return is SkippedRegistrations — a list
// of yaml-declared registrations the plugin declines at load time (e.g.
// a command that depends on optional config the plugin couldn't
// resolve). The host logs these and proceeds without the skipped
// registrations; there is no yaml fallback path.
type LoadResult struct {
	// SkippedRegistrations lists any declared registrations the plugin
	// explicitly declined at load time. Informational — the host does
	// NOT fall back to yaml-only for these; it logs and proceeds.
	SkippedRegistrations []SkippedRegistration `json:"skipped_registrations,omitempty"`
}

// SkippedRegistration is a runtime opt-out for a yaml-declared
// registration.
type SkippedRegistration struct {
	// Kind identifies the registration category, e.g. "command",
	// "slot", "component", "keybinding", "event", "crud", "mcp_server".
	Kind string `json:"kind"`
	// ID is the registration identifier from plugin.yaml.
	ID string `json:"id"`
	// Reason is a human-readable cause, surfaced in host logs.
	Reason string `json:"reason"`
}

// --- Runtime request/response types ---

// CommandExecParams is sent to the plugin for command/execute.
type CommandExecParams struct {
	Context   *ForwardContext `json:"context,omitempty"`
	Name      string          `json:"name"`
	SessionID string          `json:"session_id"`
	Args      string          `json:"args"`
	// Identity is an opaque, host-verified identity value for this
	// call. See InitParams.Identity.
	Identity json.RawMessage `json:"identity,omitempty"`
}

// CommandExecResult is returned by the plugin for command/execute.
type CommandExecResult struct {
	Action    string               `json:"action"` // "message", "noop", "error"
	Content   string               `json:"content,omitempty"`
	Envelopes []plugin.EnvelopeOut `json:"envelopes,omitempty"`
}

// EventHandleParams is sent to the plugin for event/handle.
type EventHandleParams struct {
	Context   *ForwardContext        `json:"context,omitempty"`
	Type      string                 `json:"type"`
	Source    string                 `json:"source"`
	Data      map[string]interface{} `json:"data"`
	SessionID string                 `json:"session_id,omitempty"`
	PreHook   bool                   `json:"pre_hook"` // true if host expects cancel/allow response
	// Identity is an opaque, host-verified identity value for this
	// call. See InitParams.Identity.
	Identity json.RawMessage `json:"identity,omitempty"`
}

// EventHandleResult is returned by the plugin for event/handle.
type EventHandleResult struct {
	Cancel    bool                 `json:"cancel,omitempty"` // true to cancel a pre-hook action
	Reason    string               `json:"reason,omitempty"`
	Envelopes []plugin.EnvelopeOut `json:"envelopes,omitempty"`
}

// CRUDParams is sent for all crud/* methods.
type CRUDParams struct {
	Context      *ForwardContext        `json:"context,omitempty"`
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

// --- MCP tool call (host -> plugin) ---

// MCPCallRequest is sent to the plugin for mcp/call_tool when a tool
// provided by the plugin is invoked.
type MCPCallRequest struct {
	Context   *ForwardContext        `json:"context,omitempty"`
	ToolName  string                 `json:"tool_name"`
	Arguments map[string]interface{} `json:"arguments"`
	SessionID string                 `json:"session_id,omitempty"`
	// Identity is an opaque, host-verified identity value for this
	// call. See InitParams.Identity.
	Identity json.RawMessage `json:"identity,omitempty"`
}

// MCPCallResult is returned by the plugin for mcp/call_tool.
type MCPCallResult struct {
	// Content is the tool's structured or textual output, JSON-encoded.
	Content json.RawMessage `json:"content"`
	// IsError signals the tool returned a user-visible error (not an
	// RPC-level error — use the standard RPC error for transport issues).
	IsError bool `json:"is_error,omitempty"`
	// Envelopes emitted alongside the tool result.
	Envelopes []plugin.EnvelopeOut `json:"envelopes,omitempty"`
}

// --- HTTP route handling (host -> plugin) ---

// HTTPRequest is sent to the plugin for http/handle when a registered
// plugin route is hit. Streaming is not supported on this path; use SSE
// envelopes via EventHandleResult instead.
type HTTPRequest struct {
	Context *ForwardContext `json:"context,omitempty"`
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	// RawPath preserves optional escaped separators from URL.RawPath.
	RawPath string `json:"raw_path,omitempty"`
	// RawQuery preserves the encoded query, including repeated and empty values.
	RawQuery  string            `json:"raw_query,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      []byte            `json:"body,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	// Identity carries the same opaque, host-verified value as the
	// other dispatch paths' Identity field (see InitParams.Identity).
	// Headers can already carry a raw Authorization header, but that
	// forces an HTTPHandler to parse and interpret it itself; Identity
	// gives a plugin implementing both HTTPHandler and IdentityAware
	// the caller's identity the same pre-verified way regardless of
	// which dispatch method delivered the call.
	Identity json.RawMessage `json:"identity,omitempty"`
}

// HTTPResponse is returned by the plugin for http/handle.
type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// --- Plugin migration (host -> plugin) ---

// MigrateParams is sent to the plugin for plugin/migrate when the
// installed manifest version differs from the declared version.
type MigrateParams struct {
	Context     *ForwardContext `json:"context,omitempty"`
	FromVersion string          `json:"from_version"` // installed manifest version
	ToVersion   string          `json:"to_version"`   // target manifest version
	DataDir     string          `json:"data_dir"`     // convenience — same value passed via InitParams
}

// MigrateResult is returned by the plugin for plugin/migrate. An empty
// result is equivalent to "no-op migration succeeded". Plugins that
// don't implement migration should return an empty MigrateResult.
type MigrateResult struct {
	Notes []string `json:"notes,omitempty"` // optional migration log for the host to surface
}
