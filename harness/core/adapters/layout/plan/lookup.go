package plan

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/layout"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// Table returns detached rows in authored order.
func Table() []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = r.clone()
	}
	return out
}

// Find selects a row with exact mode+variant, then mode, then layer default.
// Equal specificity is an error, never table-order precedence.
func Find(k Key) (Row, error) { return FindIn(rows, k) }

// FindIn supports validation and lookup of explicitly authored table extensions.
// Call Validate before accepting such a table. Lookup still rejects ambiguities.
func FindIn(table []Row, k Key) (Row, error) {
	if err := validateKey(k); err != nil {
		return Row{}, err
	}
	best := -1
	matches := 0
	var found Row
	for _, r := range table {
		if r.Provider != k.Provider || r.Layer != k.Layer || r.Field != k.Field {
			continue
		}
		if r.Mode != "" && r.Mode != k.Mode || r.Variant != "" && r.Variant != k.Variant {
			continue
		}
		score := 0
		if r.Mode != "" {
			score = 1
		}
		if r.Variant != "" {
			score = 2
		}
		if score == best {
			matches++
		}
		if score > best {
			best = score
			matches = 1
			found = r
		}
	}
	if best < 0 {
		return Row{}, diagnostic(k, "unsupported_feature", "no projection for this concern")
	}
	if matches > 1 {
		return Row{}, diagnostic(k, "ambiguous_layout", "equally specific rows")
	}
	return found.clone(), nil
}

// For selects every concern for a supported provider/layer/shape. Unsupported
// rows remain visible to Resolve; this is not an assertion of feature support.
func For(provider runtimes.ID, layer Layer, mode runtimes.Mode, variant layout.Variant) ([]Row, error) {
	k := Key{Provider: provider, Layer: layer, Mode: mode, Variant: variant, Field: Instructions}
	if err := validateKey(k); err != nil {
		return nil, err
	}
	var out []Row
	seen := map[Field]bool{}
	for _, r := range rows {
		if r.Provider != provider || r.Layer != layer || seen[r.Field] {
			continue
		}
		seen[r.Field] = true
		k.Field = r.Field
		selected, err := Find(k)
		if err != nil {
			return nil, err
		}
		out = append(out, selected)
	}
	return out, nil
}
func validateKey(k Key) error {
	switch k.Provider {
	case runtimes.Claude, runtimes.Codex, runtimes.OpenCode, runtimes.Antigravity:
	default:
		return diagnostic(k, "unsupported_provider", "provider is outside Wave 1 (Gemini is not Antigravity)")
	}
	if k.Mode.ACP() {
		return diagnostic(k, "unsupported_runtime", "ACP is deferred")
	}
	if k.Layer == Installed {
		if k.Provider != runtimes.Claude && k.Provider != runtimes.Codex {
			return diagnostic(k, "unsupported_layer", "installed projection is unsupported")
		}
		if k.Mode != InstallMode || k.Variant != "" {
			return diagnostic(k, "unsupported_runtime", "installed rows require the install operation")
		}
	} else if k.Layer == Boot {
		valid := false
		switch k.Provider {
		case runtimes.Claude:
			valid = k.Mode == runtimes.ModeStreamingStdio || k.Mode == runtimes.ModeSubprocessPerTurn || k.Mode == runtimes.ModePTY
		case runtimes.Codex:
			valid = k.Mode == runtimes.ModeSubprocessPerTurn || k.Mode == runtimes.ModeJSONRPCStdio
		case runtimes.OpenCode:
			valid = k.Mode == runtimes.ModeSubprocessPerTurn || k.Mode == runtimes.ModeHTTPSSE
		case runtimes.Antigravity:
			valid = k.Mode == runtimes.ModeSubprocessPerTurn
		}
		if !valid {
			return diagnostic(k, "unsupported_runtime", "provider does not support this Wave-1 transport")
		}
		if k.Variant != "" && !(k.Provider == runtimes.Claude && k.Mode == runtimes.ModeSubprocessPerTurn && k.Variant == layout.VariantBare) {
			return diagnostic(k, "unsupported_variant", "variant is not supported in this transport")
		}
	} else {
		return diagnostic(k, "unsupported_layer", "unknown layout layer")
	}
	if _, ok := Sources(k.Field); !ok {
		return diagnostic(k, "unknown_plan_field", "plan field is not registered")
	}
	return nil
}

// Request distinguishes optional content from hard requirements. Permission
// enforcement and required MCP exclusivity always refuse, never downgrade.
// PostureLookup is injected by the compiler (normally registry PostureFor).
// It checks representability without making this leaf depend on the registry.
// It must return an error for an unmapped explicit posture, never cast strings.
type PostureLookup func(runtimes.ID, string, runtimes.Mode) error

type Request struct {
	PostureID     string
	LookupPosture PostureLookup
	Key           Key
	Required      bool
	ExclusiveMCP  bool
	Components    map[string]string
}
type Resolution struct {
	Row      Row
	Omission *Diagnostic
}

func Resolve(req Request) (Resolution, error) {
	r, err := Find(req.Key)
	if err != nil {
		d, ok := err.(*Diagnostic)
		if !ok || d.Code != "unsupported_feature" {
			return Resolution{}, err
		}
		return unavailable(req, d.Reason)
	}
	if r.Capability != Supported {
		return unavailable(req, r.Reason)
	}
	if req.ExclusiveMCP {
		if req.Key.Field != MCP {
			return Resolution{}, diagnostic(req.Key, "invalid_request", "exclusive MCP applies only to MCP")
		}
		if r.ExclusiveMCP != Supported {
			return Resolution{}, diagnostic(req.Key, "unsupported_mcp_exclusivity", "MCP isolation is unsupported or unmeasured")
		}
		if r.Provider == runtimes.Claude {
			r.Locator.Argv = append(r.Locator.Argv, "--strict-mcp-config")
		}
	}
	if r.Posture != nil {
		r.Posture.PostureID = req.PostureID
		if req.PostureID != "" {
			if req.LookupPosture == nil {
				return Resolution{}, diagnostic(req.Key, "unresolved_runtime_binding", "explicit posture needs a registry lookup")
			}
			if err := req.LookupPosture(r.Provider, req.PostureID, req.Key.Mode); err != nil {
				return unavailable(req, "runtime posture is not mapped by the registry")
			}
		}
	}
	resolved, err := Expand(r.Path, req.Components)
	if err != nil {
		return Resolution{}, diagnostic(req.Key, "invalid_component", err.Error())
	}
	r.Path = resolved
	return Resolution{Row: r}, nil
}
func unavailable(req Request, reason string) (Resolution, error) {
	if reason == "" {
		reason = "feature is not projected"
	}
	if req.Required || req.ExclusiveMCP {
		return Resolution{}, diagnostic(req.Key, "unsupported_feature", reason)
	}
	return Resolution{Omission: diagnostic(req.Key, "omitted_"+string(req.Key.Field), reason)}, nil
}

// Expand substitutes only validated single components. Package support-file
// paths are separately confined by the artifact validator, never interpolated.
func Expand(pattern string, components map[string]string) (string, error) {
	if err := validatePattern(pattern); err != nil {
		return "", err
	}
	out := pattern
	for _, name := range []string{"agent", "name"} {
		token := "{" + name + "}"
		if !strings.Contains(out, token) {
			continue
		}
		value := components[name]
		if !safeComponent(value) {
			return "", fmt.Errorf("%s must be a safe single path component", name)
		}
		out = strings.ReplaceAll(out, token, value)
	}
	return out, nil
}

var component = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func safeComponent(s string) bool { return component.MatchString(s) }

func validatePattern(p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00\r\n") || path.Clean(p) != p {
		return fmt.Errorf("unsafe relative path pattern")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("unsafe relative path component")
		}
	}
	check := strings.ReplaceAll(strings.ReplaceAll(p, "{agent}", "agent"), "{name}", "name")
	if strings.ContainsAny(check, "{}") {
		return fmt.Errorf("unknown path placeholder")
	}
	return nil
}

// Validate rejects duplicates, variant-only rows, invalid selectors, unsafe
// patterns and inexact modes. Precedence has only three defined specificity
// levels, so equal specificity overlaps necessarily duplicate the same key.
func Validate(table []Row) error {
	seen := map[Key]bool{}
	for _, r := range table {
		k := Key{r.Provider, r.Layer, r.Mode, r.Variant, r.Field}
		if seen[k] {
			return diagnostic(k, "duplicate_layout", "duplicate or equally specific match")
		}
		seen[k] = true
		if r.Variant != "" && r.Mode == "" {
			return diagnostic(k, "invalid_layout", "variant requires exact mode")
		}
		mode := r.Mode
		if mode == "" {
			if r.Layer == Installed {
				mode = InstallMode
			} else {
				mode = runtimes.ModeSubprocessPerTurn
			}
		}
		check := k
		check.Mode = mode
		if err := validateKey(check); err != nil {
			return err
		}
		if r.Root != layout.RootBoot && r.Root != layout.RootHome && r.Root != layout.RootProject {
			return diagnostic(k, "invalid_layout", "unknown placement root")
		}
		if err := validatePattern(r.Path); err != nil {
			return diagnostic(k, "invalid_layout", err.Error())
		}
		if r.ModeBits == 0 && r.Form != RuntimeBinding || r.ModeBits&^uint32(0777) != 0 {
			return diagnostic(k, "invalid_layout", "exact permission bits required")
		}
		if r.Concern == "" || r.Evidence.Reference == "" {
			return diagnostic(k, "invalid_layout", "concern and evidence are required")
		}
		if r.Capability != Supported && r.Capability != Unsupported && r.Capability != Unmeasured {
			return diagnostic(k, "invalid_layout", "unknown capability status")
		}
		if r.Capability != Supported && r.Reason == "" {
			return diagnostic(k, "invalid_layout", "unsupported capability needs a reason")
		}
		switch r.Form {
		case File, Package, Slot, Link, Resource, RuntimeBinding:
		default:
			return diagnostic(k, "invalid_layout", "unknown artifact form")
		}
		if r.Capability == Supported && r.Form != RuntimeBinding && (r.Path == "" || r.Renderer == "") {
			return diagnostic(k, "invalid_layout", "supported row needs path and renderer")
		}
		if r.Form == RuntimeBinding && (r.Path != "" || r.ModeBits != 0 || r.Renderer != "" || r.Posture == nil) {
			return diagnostic(k, "invalid_layout", "runtime binding has no native file or renderer")
		}
		if r.Posture != nil && (r.Posture.Provider != r.Provider || r.Posture.Mapper != "adapters/registry.Descriptor.PostureFor" || r.Posture.PostureID != "") {
			return diagnostic(k, "invalid_layout", "invalid authored runtime posture reference")
		}
		if r.Form == Slot && r.DocumentSlot == "" {
			return diagnostic(k, "invalid_layout", "document slot required")
		}
		if (r.Field == MCP || r.Field == Credentials || r.Concern == "native-config" && r.Field != PlantingPlugin) && r.ModeBits != 0600 {
			return diagnostic(k, "invalid_layout", "config carrying MCP or credentials requires 0600")
		}
		if r.Field == Credentials && r.Form != Link {
			return diagnostic(k, "invalid_layout", "credentials must be link effects")
		}
		if r.Locator.CWD != "" && !validRoot(r.Locator.CWD) {
			return diagnostic(k, "invalid_layout", "invalid cwd root")
		}
		for name, root := range r.Locator.Env {
			if !envName.MatchString(name) || !validRoot(root) {
				return diagnostic(k, "invalid_layout", "invalid environment root binding")
			}
		}
		if r.Locator.RPCProject != "" && len(r.Locator.Argv) > 0 && r.Provider == runtimes.Codex {
			return diagnostic(k, "invalid_layout", "RPC project must not become spawn argv")
		}
		for _, arg := range r.Locator.Argv {
			if !validToken(arg) {
				return diagnostic(k, "invalid_layout", "invalid argv token")
			}
		}
	}
	return nil
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
var literalToken = regexp.MustCompile(`^[A-Za-z0-9_./:=+-]+$`)

func validRoot(r layout.Root) bool {
	return r == layout.RootBoot || r == layout.RootHome || r == layout.RootProject
}
func validToken(s string) bool {
	switch s {
	case "{B}", "{P}", "{H}", "{path}", "{agent}":
		return true
	}
	return literalToken.MatchString(s)
}
