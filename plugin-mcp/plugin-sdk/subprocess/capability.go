package subprocess

import "encoding/json"

// Capability declaration describes requested access; the host resolves it before
// spawning a plugin. The capability package owns the shared grant transport,
// descriptor catalog and scope/error helpers. A grant is discovery data; current
// execution authorization and OS sandbox policy remain host responsibilities.

// CapabilityRequest is a capability a plugin asks its host for.
//
// Name is an open string whose meaning belongs to the host, and
// Metadata is opaque JSON the host interprets according to its own
// descriptor schema. This declaration alone grants no authority.
//
// Declaration belongs in the host's manifest rather than in plugin
// code, so that what a plugin asks for is reviewable before the plugin
// runs. A host embeds this type in its own manifest schema, decides
// what to allow, and reports the outcome back over the existing
// handshake in InitParams.Grants.
//
// A plugin that lies in its manifest is bounded by what the host
// grants, not by what it claimed. The declaration is an input to the
// host's decision, not a guarantee on its own.
type CapabilityRequest struct {
	// Name identifies the capability in the host's vocabulary. The SDK
	// treats it as an opaque string.
	Name string `json:"name"`
	// Reason is a human-readable justification, surfaced to whoever
	// reviews or approves the plugin.
	Reason string `json:"reason,omitempty"`
	// Optional marks a capability the plugin can run without. A plugin
	// that sets this must degrade when the capability is not granted
	// rather than fail to load.
	Optional bool `json:"optional,omitempty"`
	// Metadata is host-defined detail about the request — a scope, a
	// path, a narrowing of any kind. Opaque to the SDK.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// HasCapability is discovery-only: false means no grant with this name.
// A present grant still requires current host authorization at execution time.
func (p *InitParams) HasCapability(name string) bool {
	for _, g := range p.Grants {
		if g.Name == name {
			return true
		}
	}
	return false
}
