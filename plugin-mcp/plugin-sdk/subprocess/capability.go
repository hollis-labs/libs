package subprocess

import "encoding/json"

// --- Capability declaration ---
//
// This file carries a declaration mechanism with an open vocabulary and
// nothing more. It is deliberately inert: nothing in the SDK reads a
// CapabilityRequest, resolves one, grants one or enforces one.
//
// The reason is the same one that keeps a contribution *kind* in the
// registry contract an open string with opaque metadata. A capability
// name like an SSH agent handle or a container runtime socket means
// something precise to an infrastructure control plane and nothing at
// all to an agent framework. Naming any of them here would move one
// host's vocabulary into the shared contract, and every other host
// would inherit it. Enforcement, sandboxing and policy are host
// concerns for the same reason, and stay host concerns.
//
// What is shared is only the shape: a plugin can say "I ask for X, for
// this reason, and I can live without it", and a host can say back
// "here is what you actually got". What X means, and whether you get
// it, belongs entirely to the host.

// CapabilityRequest is a capability a plugin asks its host for.
//
// Name is an open string whose meaning belongs to the host, and
// Metadata is opaque JSON the host interprets according to its own
// schema. The SDK defines no capability names and validates none.
//
// Declaration belongs in the host's manifest rather than in plugin
// code, so that what a plugin asks for is reviewable before the plugin
// runs. A host embeds this type in its own manifest schema, decides
// what to allow, and reports the outcome back over the existing
// handshake in InitParams.Granted.
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

// HasCapability reports whether name appears in InitParams.Granted.
//
// A false result means the host did not say the plugin has this
// capability. It does not mean the host refused. A host that predates
// capability declaration sends no Granted at all, and a host that
// grants nothing sends an empty list; the wire does not distinguish
// the two and this method does not pretend to.
//
// Use it to choose between paths that both work — the degraded one and
// the full one — the way Optional is meant to be used. Do not use it to
// decide whether to load: a capability the plugin genuinely cannot work
// without is better discovered by attempting the operation and failing
// *that* with a message naming the recovery, exactly as a missing
// credential is handled. Refusing to load against a host that simply
// does not speak this mechanism breaks a setup that was working.
func (p *InitParams) HasCapability(name string) bool {
	for _, g := range p.Granted {
		if g == name {
			return true
		}
	}
	return false
}
