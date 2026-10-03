// Package manifest defines the host-neutral declaration emitted by a plugin
// binary and reviewed before the host starts it. A declaration grants nothing:
// hosts validate their extension, resolve secrets, and enforce their own policy.
//
// Encode emits JSON, a subset of YAML, suitable for a plugin.yaml file or a
// --manifest flag. Decode reads that format without a YAML dependency. Hosts
// accepting other YAML syntax should convert it to JSON before calling Decode.
package manifest

import (
	"encoding/json"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	// SchemaVersion is independent of the subprocess wire protocol.
	SchemaVersion = 2
	Filename      = "plugin.yaml"
	Runtime       = "subprocess"
)

// Manifest is the common plugin declaration. Host-specific registrations live
// only in the corresponding extension object. No release signatures or builtin
// runtime are part of this schema; distribution archives belong in a catalog.
type Manifest struct {
	SchemaVersion int                            `json:"schema_version"`
	ID            string                         `json:"id"`
	Name          string                         `json:"name"`
	Description   string                         `json:"description,omitempty"`
	Version       string                         `json:"version"`
	License       string                         `json:"license,omitempty"`
	Homepage      string                         `json:"homepage,omitempty"`
	Repository    string                         `json:"repository,omitempty"`
	Protocol      int                            `json:"protocol"`
	Runtime       string                         `json:"runtime"`
	Entrypoint    Entrypoint                     `json:"entrypoint"`
	Capabilities  []subprocess.CapabilityRequest `json:"capabilities,omitempty"`
	Config        Config                         `json:"config,omitzero"`
	Tools         []Tool                         `json:"tools,omitempty"`
	Hosts         map[string]HostRange           `json:"hosts"`
	Cerberus      json.RawMessage                `json:"cerberus,omitempty"`
	Tangent       json.RawMessage                `json:"tangent,omitempty"`
	Nanite        json.RawMessage                `json:"nanite,omitempty"`
}

// Entrypoint names one executable inside the bundle, never a shell command.
// Hosts must additionally resolve symlinks and verify the executable on disk.
type Entrypoint struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// HostRange declares inclusive semantic-version bounds on a host contract.
// Empty Min or Max means unbounded on that side. Hosts enforce compatibility
// (including ordering of bounds and their own prerelease policy).
type HostRange struct {
	Min string `json:"min,omitempty"`
	Max string `json:"max,omitempty"`
}

// Config separates ordinary settings from host-resolved secrets. Secret values
// and defaults must never appear in a manifest. Env names are declarations,
// not permission to inherit the host process's environment.
type Config struct {
	Fields  map[string]Field  `json:"fields,omitempty"`
	Secrets map[string]Secret `json:"secrets,omitempty"`
}

type Field struct {
	Type        string   `json:"type"` // string, boolean, integer, number, select
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Env         string   `json:"env,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type Secret struct {
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Env         string `json:"env,omitempty"`
}

// Tool is a manifest-authoritative tool declaration. InputSchema is an inline
// JSON Schema with type object, not a JSON-encoded string. Effect is an open,
// required string whose vocabulary and enforcement belong to the host. The SDK
// does not infer an effect from a name or interpret one as granting authority.
type Tool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema json.RawMessage  `json:"input_schema"`
	Effect      string           `json:"effect"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations carries optional MCP hints, never authorization. Effect stays
// authoritative. Pointers distinguish an omitted hint from an explicit false;
// no defaults are inferred. Only readOnlyHint=true with destructiveHint=true
// is inconsistent.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}
