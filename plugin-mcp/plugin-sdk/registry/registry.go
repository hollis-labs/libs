// Package registry defines the protocol-2 host-authoritative contribution catalog.
// Wire structure is shared with @hollis-labs/plugin-registry. Admission and
// execution authority belong to the host; this package never grants capabilities.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const Protocol = 2
const MaxRevision = uint64(1<<53 - 1)

type Representation string

const (
	Declarative Representation = "declarative"
	Component   Representation = "component"
	Handler     Representation = "handler"
)

type Response struct {
	Protocol      int                                `json:"protocol"`
	HostInstance  string                             `json:"host_instance"`
	Revision      uint64                             `json:"revision"`
	Plugins       map[string]Plugin                  `json:"plugins"`
	Kinds         map[string]KindDescriptor          `json:"kinds"`
	Regions       map[string]RegionDescriptor        `json:"regions"`
	Contributions map[string]map[string]Contribution `json:"contributions"`
	Refusals      []Refusal                          `json:"refusals"`
}

type Plugin struct {
	OwnerGeneration string `json:"owner_generation"`
	BundleURL       string `json:"bundle_url,omitempty"`
	// BundleVersion identifies the exact bytes, not an arbitrary cache token.
	BundleVersion string    `json:"bundle_version,omitempty"`
	StylesheetURL string    `json:"stylesheet_url,omitempty"`
	Runtime       []Runtime `json:"runtime,omitempty"`
}

// Runtime has inclusive bounds; missing one side is unbounded. Both missing is invalid.
type Runtime struct {
	Name string `json:"name"`
	Min  string `json:"min,omitempty"`
	Max  string `json:"max,omitempty"`
}

type KindDescriptor struct {
	SchemaVersion        uint64           `json:"schema_version"`
	MetadataSchema       json.RawMessage  `json:"metadata_schema"`
	Representations      []Representation `json:"representations"`
	Regions              []string         `json:"regions"`
	RequiredCapabilities []string         `json:"required_capabilities"`
}

type RegionDescriptor struct {
	Kinds           []string         `json:"kinds"`
	Representations []Representation `json:"representations"`
	ContextSchema   json.RawMessage  `json:"context_schema"`
	// Ordering is host policy; default is priority 10, ascending, then manifest order.
	Ordering string `json:"ordering"`
}

type ComponentRef struct {
	Export string `json:"export"`
	Region string `json:"region"`
}

// HandlerRef is a public reviewed binding, not a token or internal execution handle.
type HandlerRef struct {
	ID string `json:"id"`
}

type Contribution struct {
	OwnerID         string          `json:"owner_id"`
	OwnerGeneration string          `json:"owner_generation"`
	LocalKey        string          `json:"local_key"`
	Kind            string          `json:"kind"`
	SchemaVersion   uint64          `json:"schema_version"`
	Required        bool            `json:"required"`
	Representation  Representation  `json:"representation"`
	Metadata        json.RawMessage `json:"metadata"`
	Component       *ComponentRef   `json:"component,omitempty"`
	Declarative     json.RawMessage `json:"declarative,omitempty"`
	Handler         *HandlerRef     `json:"handler,omitempty"`
	PublicBinding   string          `json:"public_binding,omitempty"`
}

type Refusal struct {
	OwnerID         string `json:"owner_id"`
	OwnerGeneration string `json:"owner_generation"`
	Kind            string `json:"kind"`
	LocalKey        string `json:"local_key"`
	Reason          string `json:"reason"`
	Required        bool   `json:"required"`
}

var (
	ErrProtocol            = errors.New("registry: protocol mismatch")
	ErrInvalidContribution = errors.New("registry: invalid contribution")
	ErrUnknownPlugin       = errors.New("registry: unknown owner")
	ErrCollision           = errors.New("registry: collision")
	ErrRequired            = errors.New("registry: required contribution refused")
	ErrIntegrity           = errors.New("registry: bundle integrity mismatch")
	ErrRuntime             = errors.New("registry: runtime incompatibility")
)
var name = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var digest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func NewResponse(hostInstance string, revision uint64) Response {
	return Response{Protocol: Protocol, HostInstance: hostInstance, Revision: revision,
		Plugins: map[string]Plugin{}, Kinds: map[string]KindDescriptor{}, Regions: map[string]RegionDescriptor{},
		Contributions: map[string]map[string]Contribution{}, Refusals: []Refusal{}}
}
func QualifiedKey(ownerID, localKey string) string { return ownerID + "/" + localKey }
func (c Contribution) Key() string                 { return QualifiedKey(c.OwnerID, c.LocalKey) }

// Set refuses duplicates without replacing the first declaration.
func (r Response) Set(c Contribution) error {
	if r.Contributions == nil || !name.MatchString(c.OwnerID) || !name.MatchString(c.LocalKey) || c.Kind == "" {
		return ErrInvalidContribution
	}
	byKey := r.Contributions[c.Kind]
	if byKey == nil {
		byKey = map[string]Contribution{}
		r.Contributions[c.Kind] = byKey
	}
	if _, exists := byKey[c.Key()]; exists {
		return fmt.Errorf("%w: %s/%s", ErrCollision, c.Kind, c.Key())
	}
	byKey[c.Key()] = c
	return nil
}
func Meta(v any) (json.RawMessage, error) { return json.Marshal(v) }
func validJSON(raw json.RawMessage) bool  { return len(raw) > 0 && json.Valid(raw) }
func validReps(reps []Representation) bool {
	if len(reps) == 0 {
		return false
	}
	seen := map[Representation]bool{}
	for _, r := range reps {
		if seen[r] || (r != Declarative && r != Component && r != Handler) {
			return false
		}
		seen[r] = true
	}
	return true
}

// Validate checks shape/references only. Unknown optional kinds are admission refusals.
func (r Response) Validate() error {
	if r.Protocol != Protocol {
		return ErrProtocol
	}
	if r.HostInstance == "" || r.Revision == 0 || r.Revision > MaxRevision || r.Plugins == nil || r.Kinds == nil || r.Regions == nil || r.Contributions == nil || r.Refusals == nil {
		return ErrInvalidContribution
	}
	for id, p := range r.Plugins {
		if !name.MatchString(id) || p.OwnerGeneration == "" {
			return ErrInvalidContribution
		}
		if (p.BundleURL == "") != (p.BundleVersion == "") || (p.BundleVersion != "" && !digest.MatchString(p.BundleVersion)) {
			return ErrIntegrity
		}
		seen := map[string]bool{}
		for _, rt := range p.Runtime {
			if seen[rt.Name] || rt.Name == "" || !validBounds(rt.Min, rt.Max) {
				return ErrRuntime
			}
			seen[rt.Name] = true
		}
	}
	for kind, d := range r.Kinds {
		if kind == "" || d.SchemaVersion == 0 || d.SchemaVersion > MaxRevision || !validJSON(d.MetadataSchema) || !validReps(d.Representations) || d.Regions == nil || d.RequiredCapabilities == nil {
			return ErrInvalidContribution
		}
	}
	for region, d := range r.Regions {
		if region == "" || d.Kinds == nil || !validReps(d.Representations) || !validJSON(d.ContextSchema) || (d.Ordering != "priority-ascending" && d.Ordering != "priority-descending" && d.Ordering != "manifest") {
			return ErrInvalidContribution
		}
	}
	bindings := map[string]bool{}
	for kind, byKey := range r.Contributions {
		if kind == "" || byKey == nil {
			return ErrInvalidContribution
		}
		for key, c := range byKey {
			if c.Kind != kind || !name.MatchString(c.OwnerID) || !name.MatchString(c.LocalKey) || c.Key() != key || c.OwnerGeneration == "" || c.SchemaVersion == 0 || c.SchemaVersion > MaxRevision || !validJSON(c.Metadata) {
				return ErrInvalidContribution
			}
			p, ok := r.Plugins[c.OwnerID]
			if !ok {
				return ErrUnknownPlugin
			}
			if p.OwnerGeneration != c.OwnerGeneration {
				return ErrInvalidContribution
			}
			switch c.Representation {
			case Component:
				if c.Component == nil || c.Component.Export == "" || c.Component.Region == "" || len(c.Declarative) != 0 || c.Handler != nil || p.BundleURL == "" {
					return ErrInvalidContribution
				}
			case Declarative:
				if !validJSON(c.Declarative) || c.Component != nil || c.Handler != nil {
					return ErrInvalidContribution
				}
			case Handler:
				if c.Handler == nil || c.Handler.ID == "" || c.Component != nil || len(c.Declarative) != 0 {
					return ErrInvalidContribution
				}
			default:
				return ErrInvalidContribution
			}
			if c.PublicBinding != "" {
				binding := kind + "\x00" + c.PublicBinding
				if bindings[binding] {
					return ErrCollision
				}
				bindings[binding] = true
			}
		}
	}
	for _, f := range r.Refusals {
		if f.OwnerID == "" || f.OwnerGeneration == "" || f.Kind == "" || f.LocalKey == "" || f.Reason == "" {
			return ErrInvalidContribution
		}
	}
	return nil
}

type AdmissionPolicy struct {
	Kinds    map[string]KindDescriptor
	Regions  map[string]RegionDescriptor
	Reserved func(kind, key string) bool
	// Hosts supply their schema engine. A nonempty schema cannot be silently ignored.
	ValidateMetadata func(schema, metadata json.RawMessage) error
}
type Plan struct {
	Accepted []Contribution
	Refusals []Refusal
}

func includes[T comparable](values []T, value T) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func schemaEmpty(raw json.RawMessage) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(raw, &v) == nil && v != nil && len(v) == 0
}

// Plan validates all declarations before any activation and emits named refusals.
func (r Response) Plan(policy AdmissionPolicy) (Plan, error) {
	if err := r.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Accepted: []Contribution{}, Refusals: append([]Refusal{}, r.Refusals...)}
	kinds := make([]string, 0, len(r.Contributions))
	for k := range r.Contributions {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	required := false
	for _, f := range plan.Refusals {
		required = required || f.Required
	}
	for _, kind := range kinds {
		keys := make([]string, 0, len(r.Contributions[kind]))
		for k := range r.Contributions[kind] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			c := r.Contributions[kind][key]
			reason := ""
			d, ok := policy.Kinds[kind]
			published, pubOK := r.Kinds[kind]
			switch {
			case c.OwnerID == "core" || (policy.Reserved != nil && policy.Reserved(kind, key)):
				reason = "reserved"
			case strings.HasPrefix(kind, "plugin.") && !strings.HasPrefix(kind, "plugin."+c.OwnerID+"."):
				reason = "reserved"
			case !ok || !pubOK:
				reason = "unsupported-kind"
			case d.SchemaVersion != c.SchemaVersion || published.SchemaVersion != c.SchemaVersion:
				reason = "unsupported-schema"
			case !includes(d.Representations, c.Representation) || !includes(published.Representations, c.Representation):
				reason = "unsupported-representation"
			}
			if reason == "" && c.Representation == Component {
				region := c.Component.Region
				rd, exists := policy.Regions[region]
				wire, wireOK := r.Regions[region]
				if !exists || !wireOK || !includes(d.Regions, region) || !includes(published.Regions, region) || !includes(rd.Kinds, kind) || !includes(wire.Kinds, kind) || !includes(rd.Representations, Component) || !includes(wire.Representations, Component) {
					reason = "unsupported-region"
				}
			}
			if reason == "" {
				if policy.ValidateMetadata != nil {
					if err := policy.ValidateMetadata(d.MetadataSchema, c.Metadata); err != nil {
						reason = "invalid-metadata"
					}
				} else if !schemaEmpty(d.MetadataSchema) {
					reason = "unsupported-metadata-schema"
				}
			}
			if reason != "" {
				plan.Refusals = append(plan.Refusals, Refusal{c.OwnerID, c.OwnerGeneration, c.Kind, c.LocalKey, reason, c.Required})
				required = required || c.Required
			} else {
				plan.Accepted = append(plan.Accepted, c)
			}
		}
	}
	if required {
		return plan, ErrRequired
	}
	return plan, nil
}

func BundleDigest(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// VerifyBundle must run before executing the exact immutable bytes supplied here.
func VerifyBundle(p Plugin, bytes []byte, runtimes map[string]string, allowPrerelease bool) error {
	if !digest.MatchString(p.BundleVersion) || BundleDigest(bytes) != p.BundleVersion {
		return ErrIntegrity
	}
	return CheckRuntimes(p.Runtime, runtimes, allowPrerelease)
}
func CheckRuntimes(requirements []Runtime, versions map[string]string, allowPrerelease bool) error {
	for _, r := range requirements {
		version, ok := parseVersion(versions[r.Name])
		if !ok || !validBounds(r.Min, r.Max) || (!allowPrerelease && len(version.pre) > 0) {
			return ErrRuntime
		}
		if r.Min != "" {
			min, _ := parseVersion(r.Min)
			if compare(version, min) < 0 {
				return ErrRuntime
			}
		}
		if r.Max != "" {
			max, _ := parseVersion(r.Max)
			if compare(version, max) > 0 {
				return ErrRuntime
			}
		}
	}
	return nil
}
