package agentdef

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Diagnostic identifies an invalid field with a stable machine-readable code.
type Diagnostic struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}
type ValidationError struct{ Diagnostics []Diagnostic }

func (e *ValidationError) Error() string {
	var s []string
	for _, d := range e.Diagnostics {
		s = append(s, d.Field+": "+d.Message)
	}
	return "agentdef: " + strings.Join(s, "; ")
}

type config struct {
	known      func(string) bool
	extensions func(string, string) (func(Extension) error, bool)
}
type Option func(*config)

// WithCapabilities checks requires/uses against a caller-owned vocabulary.
// Service capability IDs and tool requests are never checked against it.
func WithCapabilities(known func(string) bool) Option { return func(c *config) { c.known = known } }

// WithExtensions resolves a handler for a namespace/version pair. A handler
// validates its extension data; errors refuse even optional known extensions.
// nil handlers are unsupported, never evidence that a mandatory feature works.
// Launching consumers must apply negotiated semantics as well as validate them.
func WithExtensions(resolve func(namespace, version string) (func(Extension) error, bool)) Option {
	return func(c *config) { c.extensions = resolve }
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*(\.[a-z][a-z0-9]*(-[a-z0-9]+)*)+/[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Validate checks structure and negotiation without resolving resources,
// consulting a host, enforcing permissions or mutating the definition.
func (d *Definition) Validate(opts ...Option) error {
	var cfg config
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	var diags []Diagnostic
	add := func(code, field, message string) { diags = append(diags, Diagnostic{code, field, message}) }
	required := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			add("required", field, "is required")
		}
	}
	ref := func(field string, r Ref) {
		required(field+".uri", r.URI)
		if !digestPattern.MatchString(r.Digest) {
			add("digest", field+".digest", "must be sha256:<64 lowercase hex digits>")
		}
	}
	refs := func(field string, rs []Ref) {
		for i, r := range rs {
			ref(fmt.Sprintf("%s[%d]", field, i), r)
		}
	}
	ptr := func(field string, r *Ref) {
		if r != nil {
			ref(field, *r)
		}
	}
	if d == nil {
		add("required", "$", "definition is nil")
		return &ValidationError{diags}
	}
	if d.SchemaVersion != SchemaVersion {
		add("version", "schema_version", "unsupported schema version; expected 2")
	}
	required("definition_id", d.DefinitionID)
	required("revision", d.Revision)
	if !namePattern.MatchString(d.Name) {
		add("name", "name", "must be a lowercase hyphenated name")
	}
	required("description", d.Description)
	required("behavior.purpose", d.Behavior.Purpose)
	if strings.TrimSpace(d.Body) == "" && len(d.Behavior.Instructions) == 0 {
		add("required", "body", "instructions body or pinned behavior.instructions is required")
	}
	refs("behavior.instructions", d.Behavior.Instructions)
	refs("behavior.sops", d.Behavior.SOPs)
	for i, n := range d.Behavior.Hooks {
		if !namePattern.MatchString(n) {
			add("name", fmt.Sprintf("behavior.hooks[%d]", i), "invalid hook name")
		}
	}
	seen := map[string]bool{}
	for i, c := range d.Capabilities {
		f := fmt.Sprintf("capabilities[%d]", i)
		required(f+".id", c.ID)
		required(f+".description", c.Description)
		if seen[c.ID] {
			add("duplicate", f+".id", "duplicate service capability")
		}
		seen[c.ID] = true
		ptr(f+".input", c.Input)
		ptr(f+".output", c.Output)
	}
	for _, list := range []struct {
		field string
		names []string
	}{{"requirements.requires", d.Requirements.Requires}, {"requirements.uses", d.Requirements.Uses}} {
		seen := map[string]bool{}
		for i, n := range list.names {
			f := fmt.Sprintf("%s[%d]", list.field, i)
			if !namePattern.MatchString(n) {
				add("name", f, "invalid host capability name")
			} else if cfg.known != nil && !cfg.known(n) {
				add("capability", f, "unknown host capability")
			}
			if seen[n] {
				add("duplicate", f, "duplicate host capability")
			}
			seen[n] = true
		}
	}
	seen = map[string]bool{}
	for i, s := range d.Requirements.Skills {
		f := fmt.Sprintf("requirements.skills[%d]", i)
		if !namePattern.MatchString(s.Name) || len(s.Name) > 64 {
			add("name", f+".name", "invalid Agent Skills name")
		}
		if seen[s.Name] {
			add("duplicate", f+".name", "duplicate skill")
		}
		seen[s.Name] = true
		ref(f+".content", s.Content)
	}
	refs("requirements.resources", d.Requirements.Resources)
	refs("harness_profile.steering", d.HarnessProfile.Steering)
	refs("harness_profile.context.sources", d.HarnessProfile.Context.Sources)
	ptr("harness_profile.context.policy", d.HarnessProfile.Context.Policy)
	required("harness_profile.permissions.profile", d.HarnessProfile.Permissions.Profile)
	refs("harness_profile.approvals", d.HarnessProfile.Approvals)
	refs("harness_profile.escalation", d.HarnessProfile.Escalation)
	if d.Continuity.Mode != Durable && d.Continuity.Mode != Ephemeral {
		add("continuity", "continuity.mode", "must be durable or ephemeral")
	}
	ptr("continuity.memory_policy", d.Continuity.MemoryPolicy)
	ptr("continuity.recovery_strategy", d.Continuity.RecoveryStrategy)
	keys := make([]string, 0, len(d.Extensions))
	for k := range d.Extensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := d.Extensions[k]
		f := "extensions[" + k + "]"
		if !namespacePattern.MatchString(k) {
			add("namespace", f, "must be lowercase reverse-DNS namespace/local-name")
		}
		required(f+".version", e.Version)
		switch e.Area {
		case "behavior", "capabilities", "requirements", "harness_profile", "continuity":
		default:
			add("area", f+".area", "must name one of the five semantic areas")
		}
		if e.Data == nil {
			add("required", f+".data", "must be a JSON-compatible object")
		} else if _, err := json.Marshal(e.Data); err != nil {
			add("extension_data", f+".data", err.Error())
		}
		var handler func(Extension) error
		var supported bool
		if cfg.extensions != nil {
			handler, supported = cfg.extensions(k, e.Version)
		}
		if !supported || handler == nil {
			if e.Mandatory {
				add("mandatory_extension", f, "namespace/version is not supported")
			}
		} else if err := handler(e); err != nil {
			add("extension", f, err.Error())
		}
	}
	if len(diags) > 0 {
		return &ValidationError{diags}
	}
	return nil
}
