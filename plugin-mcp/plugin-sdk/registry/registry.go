// Package registry defines the plugin registry wire contract: what a host
// publishes so a browser can find, load and resolve the UI a plugin ships.
//
// The contract has two views of one definition — this Go view, and the
// TypeScript view in ts/packages/plugin-registry. They live in the same
// repository so the wire format is authored once. Neither generates the
// other; both pin [Protocol] and both round-trip their own types, so a host
// and a loader at genuinely different versions disagree about the protocol
// number rather than about a field.
//
// The model this serves is host-manifest-authoritative. The host publishes
// what registered; the browser resolves it. A plugin does not declare at
// runtime what it registers, and a loader never learns a registration from a
// bundle it imported.
//
// What this package deliberately does not know:
//
//   - What a contribution kind means. Kind is an open string chosen by the
//     host. One host's "envelope", "widget" and "slot" are three values of
//     that string, not three things this package has types for. Note in
//     particular that the SDK's existing [plugin.UIComponentType] enum is one
//     host's taxonomy that already lives in this module; the registry
//     contract does not build on it, and unifying the two would move a
//     host's vocabulary into the shared contract.
//   - Any host's trust, capability or isolation model.
//   - Where bundles are served from, under what CSP, and how a bundle obtains
//     shared runtime dependencies such as a React copy. Those are host
//     concerns with host answers.
//
// A host builds a [Response] from whatever it knows and serves it; this
// package supplies the types, the protocol constant and [Response.Validate].
// It deliberately contains no HTTP handler and no caching — a host owns its
// own endpoint, and the registry version counter that invalidates it is
// host state.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Protocol is the registry wire version. A host and a loader must agree on it
// exactly: a loader that meets a protocol it does not know refuses the whole
// response rather than guessing at fields, because an unreadable registry is
// not evidence that a host's plugins went away.
//
// Pinned by TestProtocolLockedAt1 here and by the matching pin on the
// TypeScript side. Bumping it is a deliberate, coordinated act.
const Protocol = 1

// Response is the whole registry document a host serves.
//
// Both maps are always present in the serialized form — a loader may treat
// them as stable dictionaries. Use [NewResponse] to get them initialized.
type Response struct {
	Protocol int `json:"protocol"`

	// Plugins describes each plugin the browser may need to load, keyed by
	// plugin id. A plugin with no bundle still belongs here; its
	// contributions are then declared-but-unresolvable, which is a state the
	// loader reports rather than hides.
	Plugins map[string]Plugin `json:"plugins"`

	// Contributions is kind -> contribution key -> contribution. The kind is
	// host-defined and opaque here. The key is unique within its kind and is
	// the name the host looks a contribution up by.
	//
	// One contribution per (kind, key). Where a host groups contributions —
	// an ordered toolbar, a priority-sorted rail — the group name and the
	// ordering belong in Meta, because grouping and order are host taxonomy
	// and no loader reads them.
	Contributions map[string]map[string]Contribution `json:"contributions"`
}

// Plugin is what the browser needs in order to load one plugin's UI.
type Plugin struct {
	// BundleURL is the ES module the browser dynamic-imports. Empty means
	// this plugin ships no browser code.
	BundleURL string `json:"bundle_url,omitempty"`

	// StylesheetURL is an optional stylesheet loaded alongside the bundle.
	StylesheetURL string `json:"stylesheet_url,omitempty"`

	// BundleVersion is an opaque cache-bust token appended to the import URL.
	//
	// It is NOT an integrity hash and nothing verifies it. The name says
	// "version" rather than "hash" on purpose: a host may legitimately derive
	// it from a modification time, and a field named hash invites a reader to
	// treat it as a content check it was never able to be.
	//
	// The host has exactly one obligation: the token must change whenever the
	// bytes at BundleURL change. URL-keyed ES module caches never re-import
	// the same URL, so a reinstall at the same path with an unchanged token
	// is invisible to the browser.
	BundleVersion string `json:"bundle_version,omitempty"`

	// Runtime declares the shared runtime a bundle expects the host to
	// provide, e.g. {"react", "^19.0.0"}. It is carried and surfaced; this
	// package and the loader do not enforce it. Recorded as a declaration so
	// a host that wants to refuse an incompatible bundle has the information,
	// and so the next reader does not mistake a carried field for a check.
	Runtime *Runtime `json:"runtime,omitempty"`
}

// Runtime names a shared runtime dependency and the range a bundle expects.
type Runtime struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Contribution is one named export a plugin offers under one kind.
type Contribution struct {
	// PluginID names the plugin that owns this contribution and must appear
	// in Response.Plugins.
	PluginID string `json:"plugin_id"`

	// Export is the named export to pull from the plugin's module. It is
	// required and explicit: a loader that infers an export name from an
	// identifier is guessing, and a host that has a display name where an
	// export name belongs has a defect the wire should not carry.
	Export string `json:"export"`

	// Meta is host-defined and opaque. It carries everything about a
	// contribution that only the host understands — a kind version, a schema
	// URL, a slot name, a priority, a label, props — and no loader reads it.
	// Keeping it raw is what stops one host's taxonomy reaching the other's.
	Meta json.RawMessage `json:"meta,omitempty"`
}

// NewResponse returns a Response at the current protocol with both maps
// initialized, so a host can populate it without nil-map checks and a loader
// always receives the two top-level keys.
func NewResponse() Response {
	return Response{
		Protocol:      Protocol,
		Plugins:       make(map[string]Plugin),
		Contributions: make(map[string]map[string]Contribution),
	}
}

// Set records one contribution, creating the kind's map on first use.
func (r Response) Set(kind, key string, c Contribution) {
	byKey, ok := r.Contributions[kind]
	if !ok {
		byKey = make(map[string]Contribution)
		r.Contributions[kind] = byKey
	}
	byKey[key] = c
}

// Meta marshals a host's contribution metadata for [Contribution.Meta].
func Meta(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("registry: marshal contribution meta: %w", err)
	}
	return json.RawMessage(raw), nil
}

// Validation failures. Each names a response a loader cannot act on.
var (
	// ErrProtocol reports a response at a protocol this build does not speak.
	ErrProtocol = errors.New("registry: protocol mismatch")
	// ErrInvalidContribution reports a structurally unusable contribution.
	ErrInvalidContribution = errors.New("registry: invalid contribution")
	// ErrUnknownPlugin reports a contribution naming a plugin the response
	// does not describe.
	ErrUnknownPlugin = errors.New("registry: contribution names an unknown plugin")
)

// Validate reports whether a response is one a loader can act on.
//
// It checks structure — the protocol, that every contribution names a plugin
// and an export, that metadata is valid JSON — and one referential rule: a
// contribution must name a plugin the response describes. That last check is
// here rather than left to the loader because a dangling plugin id is
// unresolvable by construction, and the loader's only honest response to one
// is to drop the contribution. A host finding out at serve time is better
// than a browser silently rendering less than the manifest declared.
//
// A plugin with no BundleURL is valid: a host may describe a plugin whose
// browser half is absent, and its contributions are then attributable but
// unresolved.
func (r Response) Validate() error {
	if r.Protocol != Protocol {
		return fmt.Errorf("%w: response is protocol %d, this build speaks %d", ErrProtocol, r.Protocol, Protocol)
	}
	for kind, byKey := range r.Contributions {
		if kind == "" {
			return fmt.Errorf("%w: empty contribution kind", ErrInvalidContribution)
		}
		for key, c := range byKey {
			switch {
			case key == "":
				return fmt.Errorf("%w: empty contribution key under kind %q", ErrInvalidContribution, kind)
			case c.PluginID == "":
				return fmt.Errorf("%w: %s/%s has no plugin_id", ErrInvalidContribution, kind, key)
			case c.Export == "":
				return fmt.Errorf("%w: %s/%s has no export", ErrInvalidContribution, kind, key)
			}
			if len(c.Meta) > 0 && !json.Valid(c.Meta) {
				return fmt.Errorf("%w: %s/%s has malformed meta", ErrInvalidContribution, kind, key)
			}
			if _, ok := r.Plugins[c.PluginID]; !ok {
				return fmt.Errorf("%w: %s/%s names plugin %q", ErrUnknownPlugin, kind, key, c.PluginID)
			}
		}
	}
	return nil
}
