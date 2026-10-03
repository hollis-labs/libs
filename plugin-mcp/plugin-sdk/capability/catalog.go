package capability

import (
	"slices"
	"strings"
)

const (
	ReadonlyQuery    = "readonly.query"
	ContextSource    = "context.source"
	DurableAgentWake = "durable_agent.wake"
	ReflexSeed       = "reflex.seed"
	MCPReach         = "mcp.reach"
	StorageRead      = "storage.read"
	StorageWrite     = "storage.write"
	SecretsRead      = "secrets.read"
	EgressRequest    = "egress.request"
	EventsPublish    = "events.publish"
	LogWrite         = "log.write"
)

type Effect string

const (
	Read        Effect = "read"
	Write       Effect = "write"
	Destructive Effect = "destructive"
)

// ScopeSchema closes the vocabulary of exact-match dimensions and numeric
// ceilings. All named dimensions/limits must be present, including explicit
// empty lists/zero limits for denial. Units are documented in package docs.
type ScopeSchema struct {
	Allowlists []string `json:"allowlists"`
	Limits     []string `json:"limits"`
}

// Descriptor defines semantics; it does not grant authority or install a
// handler. Proposed marks reverse-service entries pending catalog ratification.
type Descriptor struct {
	Name          string      `json:"name"`
	SchemaVersion int         `json:"schema_version"`
	ScopeSchema   ScopeSchema `json:"scope_schema"`
	Description   string      `json:"description"`
	EffectCeiling Effect      `json:"effect_ceiling"`
	Operations    []string    `json:"operations"`
	Proposed      bool        `json:"proposed"`
}

// ValidateScope rejects unknown versions, dimensions, effects and operations.
func (d Descriptor) ValidateScope(version int, s Scope) error {
	if version != d.SchemaVersion {
		return refusal(UnsupportedCapability, d.Name)
	}
	if err := s.Validate(d.Name); err != nil {
		return err
	}
	for k := range s.Allowlists {
		if !slices.Contains(d.ScopeSchema.Allowlists, k) {
			return refusal(ScopeDenied, d.Name)
		}
	}
	for k := range s.Limits {
		if !slices.Contains(d.ScopeSchema.Limits, k) {
			return refusal(ScopeDenied, d.Name)
		}
	}
	for _, k := range d.ScopeSchema.Allowlists {
		if _, ok := s.Allowlists[k]; !ok {
			return refusal(ScopeDenied, d.Name)
		}
	}
	for _, k := range d.ScopeSchema.Limits {
		if _, ok := s.Limits[k]; !ok {
			return refusal(ScopeDenied, d.Name)
		}
	}
	for _, op := range s.Allowlists["operations"] {
		if !slices.Contains(d.Operations, op) {
			return refusal(ScopeDenied, d.Name)
		}
	}
	for _, effect := range s.Allowlists["effects"] {
		if !effectWithin(Effect(effect), d.EffectCeiling) {
			return refusal(ScopeDenied, d.Name)
		}
	}
	return nil
}
func effectWithin(effect, ceiling Effect) bool {
	rank := func(e Effect) int {
		switch e {
		case Read:
			return 1
		case Write:
			return 2
		case Destructive:
			return 3
		}
		return 0
	}
	return rank(effect) > 0 && rank(effect) <= rank(ceiling)
}
func descriptor(name, description string, effect Effect, proposed bool, ops, dimensions, limits []string) Descriptor {
	return Descriptor{name, 1, ScopeSchema{append([]string{"operations", "targets", "effects"}, dimensions...), limits}, description, effect, ops, proposed}
}

// SharedDescriptors returns independent copies of the seed and proposed
// catalogs. Hosts opt into a supported subset; presence here is not permission.
func SharedDescriptors() []Descriptor {
	return []Descriptor{
		descriptor(ReadonlyQuery, "Fixed read DTOs on approved resources; no SQL or arbitrary endpoints.", Read, false, []string{"host/readonly/query"}, []string{"resources", "sessions", "visibility"}, []string{"rows", "request_bytes", "response_bytes", "deadline_ms"}),
		descriptor(ContextSource, "Declared context sources with ownership and retrieval budgets.", Read, false, []string{"register", "retrieve"}, []string{"agents", "sessions", "sources", "mounts"}, []string{"response_bytes"}),
		descriptor(DurableAgentWake, "Wake approved agents; no provisioning, commands or scheduler access.", Write, false, []string{"wake"}, []string{"agents"}, []string{"request_bytes", "rate_per_minute"}),
		descriptor(ReflexSeed, "Install exact declared seeds on approved agents.", Write, false, []string{"install"}, []string{"agents", "seeds"}, []string{"request_bytes"}),
		descriptor(MCPReach, "Discover/call/cancel reviewed tools with pinned definitions and no delegation cycles.", Destructive, false, []string{"host/mcp/list_tools", "host/mcp/call_tool", "host/mcp/cancel_call"}, []string{"server_tools", "definition_revisions", "sessions"}, []string{"request_bytes", "response_bytes", "deadline_ms", "concurrency", "rate_per_minute", "call_depth"}),
		descriptor(StorageRead, "Owner-private logical KV keys.", Read, true, []string{"host/storage/get"}, []string{"keys"}, []string{"response_bytes"}),
		descriptor(StorageWrite, "Owner-private KV mutations with expected revision and operation key.", Destructive, true, []string{"host/storage/put", "host/storage/delete"}, []string{"keys"}, []string{"request_bytes"}),
		descriptor(SecretsRead, "Exact broker secret references; external custody and redaction.", Read, true, []string{"host/secrets/get"}, []string{"secret_refs"}, []string{"lifetime_ms", "response_bytes"}),
		descriptor(EgressRequest, "Reviewed HTTPS destinations/methods; host checks DNS, redirects and proxies.", Destructive, true, []string{"host/egress/request"}, []string{"destinations", "methods"}, []string{"request_bytes", "response_bytes", "deadline_ms"}),
		descriptor(EventsPublish, "Owner-namespaced declared schemas; no core/gate impersonation.", Write, true, []string{"host/events/publish"}, []string{"event_schemas"}, []string{"request_bytes", "rate_per_minute"}),
		descriptor(LogWrite, "Bounded redacted logging to the host sink.", Write, true, []string{"host/log"}, []string{"levels"}, []string{"request_bytes", "rate_per_minute"}),
	}
}

// Catalog publishes a host-selected subset and private copies of extensions.
// Extensions cannot shadow a shared name, even when that name is unsupported.
type Catalog struct{ descriptors map[string]Descriptor }

func NewCatalog(supported []string, extensions []Descriptor) (*Catalog, error) {
	shared := map[string]Descriptor{}
	for _, d := range SharedDescriptors() {
		shared[d.Name] = d
	}
	c := &Catalog{descriptors: map[string]Descriptor{}}
	for _, name := range supported {
		d, ok := shared[name]
		if !ok {
			return nil, refusal(UnsupportedCapability, name)
		}
		if _, ok := c.descriptors[name]; ok {
			return nil, refusal(InvalidRequest, name)
		}
		c.descriptors[name] = d
	}
	for _, d := range extensions {
		if _, reserved := shared[d.Name]; reserved {
			return nil, refusal(UnsupportedCapability, d.Name)
		}
		if !extensionName(d.Name) || d.SchemaVersion < 1 || d.Description == "" || !effectWithin(Read, d.EffectCeiling) || len(d.Operations) == 0 {
			return nil, refusal(InvalidRequest, d.Name)
		}
		if _, ok := c.descriptors[d.Name]; ok {
			return nil, refusal(InvalidRequest, d.Name)
		}
		if !validNames(d.Operations) || !validNames(d.ScopeSchema.Allowlists) || !validNames(d.ScopeSchema.Limits) || !slices.Contains(d.ScopeSchema.Allowlists, "operations") || !slices.Contains(d.ScopeSchema.Allowlists, "targets") || !slices.Contains(d.ScopeSchema.Allowlists, "effects") {
			return nil, refusal(InvalidRequest, d.Name)
		}
		c.descriptors[d.Name] = cloneDescriptor(d)
	}
	return c, nil
}
func extensionName(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 3 || (parts[0] != "host" && parts[0] != "plugin") {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, ch := range p {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return false
			}
		}
	}
	return true
}
func validNames(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || v == "*" || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func cloneDescriptor(d Descriptor) Descriptor {
	d.Operations = slices.Clone(d.Operations)
	d.ScopeSchema.Allowlists = slices.Clone(d.ScopeSchema.Allowlists)
	d.ScopeSchema.Limits = slices.Clone(d.ScopeSchema.Limits)
	return d
}
func (c *Catalog) Lookup(name string, version int) (Descriptor, error) {
	if c == nil {
		return Descriptor{}, refusal(UnsupportedCapability, name)
	}
	d, ok := c.descriptors[name]
	if !ok || d.SchemaVersion != version {
		return Descriptor{}, refusal(UnsupportedCapability, name)
	}
	return cloneDescriptor(d), nil
}
