package render

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	agy "github.com/hollis-labs/substrate/harness/adapters/antigravity/nativefiles"
	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	codex "github.com/hollis-labs/substrate/harness/adapters/codex/nativefiles"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	opencode "github.com/hollis-labs/substrate/harness/adapters/opencode/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// Render freezes resolved plan rows and content into an artifact tree and typed
// launch/effect contracts. It never applies the tree or resolves a source.
func Render(req Request) (Result, error) {
	selected, err := layout.For(req.Provider, req.Layer, req.Mode, req.Variant)
	if err != nil {
		return Result{}, err
	}
	for _, root := range []layout.Root{layout.RootBoot, layout.RootProject, layout.RootHome} {
		if value := req.Roots[root]; value != "" {
			ctx := contract.Context{Provider: string(req.Provider), Mode: string(req.Mode), Concern: "roots"}
			if err := contract.ValidateDirectories(ctx, []string{value}); err != nil || path.Clean(value) != value || value == "/" || !safeRootText(value) {
				return Result{}, refuse(req, layout.Resources, "unsafe_root", "supplied root must be a canonical absolute directory")
			}
		}
	}
	if req.Layer == layout.Boot && req.Roots[layout.RootBoot] != "" && strings.EqualFold(req.Roots[layout.RootBoot], req.Roots[layout.RootProject]) {
		return Result{}, refuse(req, layout.Resources, "unsafe_root", "boot root must differ from project root")
	}
	count := len(req.Overlays) + len(req.Inputs)
	for _, in := range req.Inputs {
		count += len(in.Content.Package.Entries)
	}
	if count > MaxTreeEntries {
		return Result{}, refuse(req, layout.Resources, "input_limit", "entry count exceeds render limit")
	}
	out := Result{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Variant: req.Variant, Root: layout.RootBoot, RootMode: 0700, Tree: artifact.Tree{Provenance: artifact.Provenance{Source: "workspace/render"}}, Binding: Binding{Environment: map[string]string{}, RPCProject: map[string]string{}}}
	if req.Layer == layout.Installed {
		out.Root = layout.RootHome
		out.RootMode = 0
	}
	switch req.Credentials {
	case "", CredentialAvailable:
	case CredentialMissing:
		return Result{}, refuse(req, layout.Credentials, "missing_credentials", "runtime credentials are unavailable")
	case CredentialDenied:
		return Result{}, refuse(req, layout.Credentials, "denied_credentials", "credential provisioning was denied")
	default:
		return Result{}, refuse(req, layout.Credentials, "invalid_credentials", "unknown credential availability")
	}
	if req.Credentials == "" && req.Layer == layout.Boot {
		out.Preparations = append(out.Preparations, Preparation{Provider: req.Provider, Kind: "credential-availability"})
	}
	if req.Provider != runtimes.Claude && !reflect.ValueOf(req.Native.Claude).IsZero() || req.Provider != runtimes.Codex && !reflect.ValueOf(req.Native.Codex).IsZero() || req.Provider != runtimes.OpenCode && !reflect.ValueOf(req.Native.OpenCode).IsZero() {
		return Result{}, refuse(req, layout.Settings, "provider_input_mismatch", "native input belongs to another provider")
	}
	if req.InstructionPointer && (req.Provider != runtimes.Claude || req.Layer != layout.Boot) {
		return Result{}, refuse(req, layout.Instructions, "invalid_pointer", "instruction pointer is a Claude boot option")
	}
	configRows := map[string]layout.Row{}
	seenFields := map[layout.Field]bool{}
	claims := []contract.Claim{}
	operatorMatches := make([]bool, len(req.Native.OperatorKeyPaths))
	locators := []layout.Row{}
	hasMCP := false
	// Canonical table order defines binding token order, independently of caller
	// collection order. Resource names break ties within a repeated field.
	order := map[layout.Field]int{}
	for i, row := range selected {
		order[row.Field] = i
	}
	inputs := slices.Clone(req.Inputs)
	inputKey := func(in Input) (layout.Field, string) {
		if in.Resolved.Omission != nil {
			return in.Resolved.Omission.Concern, ""
		}
		if len(in.Resolved.Effects) > 0 {
			return in.Resolved.Effects[0].Field, in.Resolved.Effects[0].Path
		}
		return in.Resolved.Row.Field, in.Resolved.Row.Path
	}
	slices.SortStableFunc(inputs, func(a, b Input) int {
		af, ap := inputKey(a)
		bf, bp := inputKey(b)
		if n := cmp.Compare(order[af], order[bf]); n != 0 {
			return n
		}
		return cmp.Compare(ap, bp)
	})
	for _, input := range inputs {
		res := input.Resolved
		if !utf8.ValidString(input.Content.Pin.Source) || !utf8.ValidString(input.Content.Pin.Revision) {
			return Result{}, refuse(req, layout.Resources, "invalid_pin", "content pin must be valid UTF-8")
		}
		if res.Omission != nil {
			d := res.Omission
			if res.Row.Provider != "" || len(res.Effects) > 0 || d.Provider != req.Provider || d.Layer != req.Layer || d.Mode != req.Mode {
				return Result{}, refuse(req, d.Concern, "invalid_resolution", "omission does not match the target")
			}
			out.Diagnostics = append(out.Diagnostics, Diagnostic{Code: DiagnosticCode(d.Code), Class: ClassOmission, Provider: d.Provider, Mode: d.Mode, Concern: d.Concern, Reason: d.Reason})
			continue
		}
		if len(res.Effects) > 0 {
			if res.Row.Provider != "" || len(input.Content.Body) > 0 || len(input.Content.Package.Entries) > 0 {
				return Result{}, refuse(req, layout.Credentials, "invalid_resolution", "credential effects cannot contain managed content")
			}
			for _, effect := range res.Effects {
				if err := validateRow(req, effect); err != nil {
					return Result{}, err
				}
				if effect.Form != layout.Link || effect.CredentialPolicy != layout.LinkOnlyNeverWrite {
					return Result{}, refuse(req, layout.Credentials, "invalid_effect", "credential destination must be a preserve-only link")
				}
				for _, prior := range out.Effects {
					if strings.EqualFold(prior.Path, effect.Path) {
						return Result{}, refuse(req, layout.Credentials, "path_collision", "credential effect is repeated")
					}
				}
				out.Effects = append(out.Effects, effect.Clone())
				out.Preparations = append(out.Preparations, Preparation{Provider: req.Provider, Kind: "credential-link", Destination: effect.Path, Policy: effect.CredentialPolicy})
			}
			continue
		}
		r := res.Row
		if err := validateRow(req, r); err != nil {
			return Result{}, err
		}
		if seenFields[r.Field] && r.Field != layout.Skills && r.Field != layout.Commands && r.Field != layout.Prompts && r.Field != layout.Subagents {
			return Result{}, refuse(req, r.Field, "duplicate_input", "plan field is repeated")
		}
		seenFields[r.Field] = true
		if r.Field == layout.MCP {
			hasMCP = true
		}
		if r.Posture != nil {
			p := *r.Posture
			out.Binding.Posture = &p
		}
		if r.Form == layout.RuntimeBinding {
			if len(input.Content.Body) > 0 || len(input.Content.Package.Entries) > 0 {
				return Result{}, refuse(req, r.Field, "invalid_input", "runtime binding cannot carry file content")
			}
			continue
		}
		locators = append(locators, r)
		claims = append(claims, contract.SerializerClaim(r.Path, serializerOwner(req.Provider), r.Composition))
		if r.Renderer == "claude-settings" || r.Renderer == "codex-config" || r.Renderer == "opencode-config" {
			if len(input.Content.Body) > 0 || len(input.Content.Package.Entries) > 0 {
				return Result{}, refuse(req, r.Field, "invalid_input", "native documents require typed slots, not whole-file content")
			}
			configRows[r.Path] = r
			continue
		}
		if r.Form == layout.Package {
			entries, diags, err := packageEntries(req, r, input.Content)
			if err != nil {
				return Result{}, err
			}
			out.Tree.Entries = append(out.Tree.Entries, entries...)
			out.Diagnostics = append(out.Diagnostics, diags...)
			continue
		}
		if r.Field == layout.Commands || r.Field == layout.Prompts || r.Field == layout.Subagents {
			if input.Content.Pin.Source == "" || input.Content.Pin.Revision == "" {
				return Result{}, refuse(req, r.Field, "unpinned_content", "resource registration must supply a frozen pin")
			}
		}
		body, err := fileBytes(req, r, input.Content.Body)
		if err != nil {
			return Result{}, err
		}
		if req.Layer == layout.Installed && r.Field == layout.Instructions {
			if ValidateComponent(req.DefinitionName) != nil {
				return Result{}, refuse(req, r.Field, "invalid_definition_name", "installed instruction marker requires an explicit definition identifier")
			}
			if len(body) == 0 {
				out.Diagnostics = append(out.Diagnostics, Diagnostic{Class: ClassOmission, Code: "omitted_empty_installed_instructions", Provider: req.Provider, Mode: req.Mode, Concern: r.Field, Reason: "empty installed instruction body emits no generated document"})
				continue
			}
			body = append([]byte("<!-- Generated by `cairn install` from profile "+strconv.Quote(req.DefinitionName)+". -->\n\n"), body...)
		}
		e := entry(r, body, input.Content.Pin)
		if r.Field == layout.MCP || r.Field == layout.PlantingPlugin {
			var object map[string]any
			// Keep this invariant check at the serializer boundary so future encoders
			// cannot mint ownership metadata from malformed document bytes.
			if json.Unmarshal(body, &object) != nil {
				return Result{}, refuse(req, r.Field, "invalid_native_document", "native JSON serializer produced an invalid object")
			}
			if err := documentOwnership(req, r, contract.ObjectKeyPaths(object), selected, &e, operatorMatches); err != nil {
				return Result{}, err
			}
		}
		out.Tree.Entries = append(out.Tree.Entries, e)
	}
	if req.InstructionPointer && (!seenFields[layout.Instructions] || !seenFields[layout.NeutralInstructions]) {
		return Result{}, refuse(req, layout.Instructions, "missing_content", "explicit instruction pointer requires its neutral body")
	}
	ctx := contract.Context{Provider: string(req.Provider), Mode: string(req.Mode), Concern: "composition"}
	if err := contract.ValidateComposition(ctx, claims, reservedPaths(selected)); err != nil {
		return Result{}, err
	}
	if len(req.Native.Servers) > 0 && !hasMCP {
		return Result{}, refuse(req, layout.MCP, "unresolved_input", "MCP bindings need a resolved MCP row")
	}
	hasNativeSettings := !reflect.ValueOf(req.Native.Claude).IsZero() || !reflect.ValueOf(req.Native.Codex).IsZero() || !reflect.ValueOf(req.Native.OpenCode).IsZero()
	if hasNativeSettings && len(configRows) == 0 {
		return Result{}, refuse(req, layout.Settings, "unresolved_input", "native settings require their resolved document owner")
	}
	if err := nativePosture(&req, &out); err != nil {
		return Result{}, err
	}
	configPaths := slices.Sorted(maps.Keys(configRows))
	for _, configPath := range configPaths {
		r := configRows[configPath]
		doc, err := configDocument(req, r)
		if err != nil {
			return Result{}, err
		}
		if doc.Bytes == nil {
			doc.Bytes = []byte{}
		}
		e := entry(r, doc.Bytes, Pin{})
		if err := documentOwnership(req, r, doc.KeyPaths, selected, &e, operatorMatches); err != nil {
			return Result{}, err
		}
		out.Tree.Entries = append(out.Tree.Entries, e)
	}
	for _, matched := range operatorMatches {
		if !matched {
			return Result{}, refuse(req, layout.Settings, "invalid_ownership", "operator key path must match an emitted leaf exactly")
		}
	}
	for _, overlay := range artifact.CloneEntries(req.Overlays) {
		if err := contract.ValidateComposition(ctx, []contract.Claim{contract.OverlayClaim(contract.Overlay{Path: overlay.Path})}, reservedPaths(selected)); err != nil {
			return Result{}, err
		}
		if err := ValidateRelPath(overlay.Path); err != nil {
			return Result{}, refuse(req, layout.Resources, "unsafe_path", "overlay destination is unsafe")
		}
		if reserved(selected, overlay.Path) {
			return Result{}, refuse(req, layout.Resources, "reserved_destination", "overlay claims a native or credential destination")
		}
		if overlay.ContentRef != nil {
			return Result{}, refuse(req, layout.Resources, "unresolved_content", "overlay content must already be resolved")
		}
		mode, diag, err := normalizeMode(req, overlay, layout.Resources, false, fs.FileMode(layout.FileMode))
		if err != nil {
			return Result{}, err
		}
		overlay.Mode = mode
		if diag != nil {
			out.Diagnostics = append(out.Diagnostics, *diag)
		}
		overlay.Provenance = artifact.Provenance{Source: "workspace/render"}
		overlay.Ownership = artifact.Ownership{EntryID: "workspace.render:" + overlay.Path, GroupID: "workspace.render:overlay"}
		out.Tree.Entries = append(out.Tree.Entries, overlay)
	}
	if err := bind(req, &out, locators); err != nil {
		return Result{}, err
	}
	if req.Layer == layout.Boot && req.Provider == runtimes.Codex && (out.Binding.Posture == nil || out.Binding.Posture.Posture == "") {
		// PTY remains unsupported by the table. Retain the no-policy interactive
		// rule here for a future measured PTY row; headless modes fail closed.
		if req.Mode != runtimes.ModePTY && !hostDefault(req.Native.Codex) {
			return Result{}, refuse(req, layout.Permissions, "posture_required", "headless Codex requires a bound posture or explicit host-declared native default")
		}
		reason := "posture absent: no native policy emitted; interactive runtime default applies"
		if req.Native.Codex.ApprovalPolicy != "" || req.Native.Codex.SandboxMode != "" {
			reason = "posture absent: host-declared native policy retained; runtime posture overrides absent"
		}
		for _, slot := range req.Native.Codex.Slots {
			if slot.Key == "approval_policy" || slot.Key == "sandbox_mode" {
				reason = "posture absent: host-declared native policy retained; runtime posture overrides absent"
			}
		}
		out.Diagnostics = append(out.Diagnostics, Diagnostic{Code: "posture_absent", Provider: req.Provider, Mode: req.Mode, Concern: layout.Permissions, Reason: reason})
	}
	// Revalidate after package prefixing and native generation: those operations
	// can exceed path depth/length even when the source-relative names are safe.
	// Explicit directories, including empty packages, retain one stable owner.
	files := map[string]artifact.Entry{}
	for _, e := range out.Tree.Entries {
		if err := ValidateRelPath(e.Path); err != nil {
			return Result{}, refuse(req, layout.Resources, "unsafe_path", "artifact destination is unsafe")
		}
		// Checking every component here also excludes all synthesized ancestors.
		if credential(e.Path) {
			return Result{}, refuse(req, layout.Credentials, "credential_write", "credential destinations cannot be managed entries")
		}
		if _, exists := files[contract.FoldPath(e.Path)]; exists {
			return Result{}, refuse(req, layout.Resources, "path_collision", "multiple entries claim the same destination")
		}
		files[contract.FoldPath(e.Path)] = e
	}
	if _, err := synthesizeDirectories(req, out.Tree.Entries, files); err != nil {
		return Result{}, err
	}
	out.Tree.Entries = nil
	for _, e := range files {
		if e.Kind == artifact.EntryFile {
			sum := sha256.Sum256(e.Bytes)
			e.Digest = artifact.Digest{Algorithm: "sha256", Hex: hex.EncodeToString(sum[:])}
		} else {
			e.Digest = artifact.Digest{}
		}
		out.Tree.Entries = append(out.Tree.Entries, e)
	}
	normalized, err := artifact.Normalize(out.Tree.Entries)
	if err != nil {
		return Result{}, refuse(req, layout.Resources, "invalid_artifact", "artifact entries are invalid or collide")
	}
	out.Tree.Entries = normalized
	for i := range out.Diagnostics {
		if out.Diagnostics[i].Class == "" {
			out.Diagnostics[i].Class = ClassInformational
		}
	}
	return out, nil
}
func entry(r layout.Row, body []byte, pin Pin) artifact.Entry {
	return artifact.Entry{Path: r.Path, Kind: artifact.EntryFile, Mode: fs.FileMode(r.ModeBits), Bytes: bytes.Clone(body), Ownership: artifact.Ownership{EntryID: "workspace.render:" + r.Path, GroupID: "workspace.render:" + r.Renderer}, Provenance: artifact.Provenance{Source: "workspace/render", SourcePath: pin.Source, Revision: pin.Revision, Note: string(r.Field)}}
}
func validateRow(req Request, r layout.Row) error {
	if r.Provider != req.Provider || r.Layer != req.Layer {
		return refuse(req, r.Field, "invalid_resolution", "row does not match target")
	}
	expected, err := layout.Find(layout.Key{Provider: req.Provider, Layer: req.Layer, Mode: req.Mode, Variant: req.Variant, Field: r.Field})
	if err != nil {
		return err
	}
	// Capability must match its authored value and be eligible for emission.
	if r.Mode != expected.Mode || r.Variant != expected.Variant || r.DocumentSlot != expected.DocumentSlot || r.Concern != expected.Concern || r.Capability != expected.Capability || r.Capability != layout.Supported || r.ExclusiveMCP != expected.ExclusiveMCP || r.Form != expected.Form || r.Renderer != expected.Renderer || r.ModeBits != expected.ModeBits || r.Root != expected.Root || r.Composition != expected.Composition || r.CredentialPolicy != expected.CredentialPolicy || !patternMatches(expected.Path, r.Path) {
		return refuse(req, r.Field, "invalid_resolution", "resolved row differs from the authored table")
	}
	if r.Form != layout.RuntimeBinding && ValidateRelPath(r.Path) != nil {
		return refuse(req, r.Field, "unsafe_path", "row destination is unsafe")
	}
	usesAgent := strings.Contains(expected.Path, "{agent}") || slices.Contains(expected.Locator.Argv, "{agent}") || expected.Locator.RPCProject == "directory/agent"
	if usesAgent && (ValidateComponent(req.Agent) != nil || strings.HasPrefix(req.Agent, "-")) {
		return refuse(req, r.Field, "invalid_component", "agent used by a selected path or locator must be a safe non-option component")
	}
	if strings.Contains(expected.Path, "{agent}") {
		concrete, err := layout.Expand(expected.Path, map[string]string{"agent": req.Agent})
		if err != nil || concrete != r.Path {
			return refuse(req, r.Field, "agent_mismatch", "rendered agent ID must match launch selection")
		}
	}
	loc := expected.Locator
	if req.Provider == runtimes.Claude && r.Field == layout.MCP && len(r.Locator.Argv) > 0 && r.Locator.Argv[len(r.Locator.Argv)-1] == "--strict-mcp-config" {
		loc.Argv = append(slices.Clone(loc.Argv), "--strict-mcp-config")
	}
	if !reflect.DeepEqual(loc, r.Locator) {
		return refuse(req, r.Field, "invalid_resolution", "launch locator differs from the authored table")
	}
	if expected.Posture == nil && r.Posture != nil || expected.Posture != nil && (r.Posture == nil || r.Posture.Provider != expected.Posture.Provider || r.Posture.Mapper != expected.Posture.Mapper) {
		return refuse(req, r.Field, "invalid_resolution", "runtime posture reference differs from the table")
	}
	return nil
}
func patternMatches(pattern, rel string) bool     { return matchPattern(pattern, rel, false) }
func patternMatchesFold(pattern, rel string) bool { return matchPattern(pattern, rel, true) }
func matchPattern(pattern, rel string, fold bool) bool {
	if pattern == rel {
		return true
	}
	exp := regexp.QuoteMeta(pattern)
	exp = strings.ReplaceAll(exp, regexp.QuoteMeta("{name}"), `[A-Za-z0-9_][A-Za-z0-9_.-]*`)
	exp = strings.ReplaceAll(exp, regexp.QuoteMeta("{agent}"), `[A-Za-z0-9_][A-Za-z0-9_.-]*`)
	if fold {
		exp = "(?i)" + exp
	}
	ok, _ := regexp.MatchString("^"+exp+"$", rel)
	return ok
}

// IsCredentialDestination is the table's sole credential exclusion rule.
func IsCredentialDestination(rel string) bool { return layout.IsCredentialDestination(rel) }
func credential(rel string) bool              { return IsCredentialDestination(rel) }

// reserved retains folded namespace/ancestor checks as defense in depth
// beyond the shared composition comparison, including package patterns.
func reserved(rows []layout.Row, rel string) bool {
	if credential(rel) {
		return true
	}
	for _, r := range rows {
		if r.Path != "" && (patternMatchesFold(r.Path, rel) || strings.HasPrefix(contract.FoldPath(r.Path), contract.FoldPath(rel+"/"))) {
			return true
		}
		if r.Form == layout.Package {
			prefix := strings.TrimSuffix(r.Path, "/SKILL.md")
			parts := strings.Split(rel, "/")
			for i := range parts {
				if patternMatchesFold(prefix, strings.Join(parts[:i+1], "/")) {
					return true
				}
			}
		}
	}
	return false
}
func fileBytes(req Request, r layout.Row, body []byte) ([]byte, error) {
	switch r.Renderer {
	case "claude-instructions":
		if req.InstructionPointer {
			if body != nil {
				return nil, refuse(req, r.Field, "pointer_body", "pointer input cannot also supply an instruction body")
			}
			return claude.Pointer(), nil
		}
		if body == nil {
			return nil, refuse(req, r.Field, "missing_content", "instruction body must be explicitly supplied")
		}
		return claude.Instructions(body), nil
	case "instructions", "kickoff", "claude-command", "claude-subagent":
		if body == nil {
			return nil, refuse(req, r.Field, "missing_content", "file content must be explicit, including an empty body")
		}
		return bytes.Clone(body), nil
	case "claude-pointer":
		return claude.Pointer(), nil
	case "opencode-agent":
		return opencode.Agent(opencode.AgentInput{Mode: string(req.Mode), Name: req.Agent, Body: body})
	case "antigravity-plugin":
		return agy.Plugin(), nil
	case "claude-mcp":
		return claude.MCP(claude.MCPInput{Mode: string(req.Mode), Servers: req.Native.Servers})
	case "antigravity-mcp":
		return agy.MCP(agy.MCPInput{Mode: string(req.Mode), Servers: req.Native.Servers})
	default:
		return nil, refuse(req, r.Field, "unsupported_renderer", "row has no native serializer")
	}
}
func configDocument(req Request, r layout.Row) (contract.Document, error) {
	switch r.Renderer {
	case "claude-settings":
		in := req.Native.Claude
		in.Mode = string(req.Mode)
		return claude.SettingsDocument(in)
	case "codex-config":
		in := req.Native.Codex
		if len(in.Servers) > 0 {
			return contract.Document{}, refuse(req, layout.MCP, "duplicate_input", "MCP bindings must use the shared server input")
		}
		in.Mode = string(req.Mode)
		if req.Layer == layout.Installed {
			in.Encoding = codex.InstalledEncoding
		} else if in.Encoding != codex.BootEncoding {
			return contract.Document{}, refuse(req, r.Field, "invalid_encoding", "boot config must use its native boot encoding")
		}
		in.Servers = req.Native.Servers
		return codex.ConfigDocument(in)
	case "opencode-config":
		in := req.Native.OpenCode
		if len(in.Servers) > 0 {
			return contract.Document{}, refuse(req, layout.MCP, "duplicate_input", "MCP bindings must use the shared server input")
		}
		in.Mode = string(req.Mode)
		in.Servers = req.Native.Servers
		return opencode.ConfigDocument(in)
	}
	return contract.Document{}, refuse(req, r.Field, "unsupported_renderer", "native document has no serializer")
}
func packageEntries(req Request, r layout.Row, content Content) ([]artifact.Entry, []Diagnostic, error) {
	if content.Pin.Source == "" || content.Pin.Revision == "" {
		return nil, nil, refuse(req, r.Field, "unpinned_content", "skill package must supply a frozen pin")
	}
	entries := artifact.CloneEntries(content.Package.Entries)
	slices.SortStableFunc(entries, func(a, b artifact.Entry) int { return cmp.Compare(a.Path, b.Path) })
	if len(entries) == 0 {
		return nil, nil, refuse(req, r.Field, "missing_content", "skill package is empty")
	}
	base := path.Dir(r.Path)
	main := false
	diags := []Diagnostic{}
	for i := range entries {
		e := &entries[i]
		if ValidateRelPath(e.Path) != nil || e.ContentRef != nil {
			return nil, nil, refuse(req, r.Field, "unsafe_package", "package entries must be safe and already resolved")
		}
		if e.Path == "SKILL.md" && e.Kind == artifact.EntryFile {
			main = true
		}
		modeEntry := *e
		modeEntry.Path = path.Join(base, e.Path)
		applied, diag, err := normalizeMode(req, modeEntry, r.Field, true, fs.FileMode(r.ModeBits))
		if err != nil {
			return nil, nil, err
		}
		if diag != nil {
			diag.Entry = path.Join(base, e.Path)
			diags = append(diags, *diag)
		}
		e.Mode = applied
		e.Path = path.Join(base, e.Path)
		e.Ownership = artifact.Ownership{EntryID: "workspace.render:" + e.Path, GroupID: "workspace.render:" + base}
		e.Provenance = artifact.Provenance{Source: content.Pin.Source, Revision: content.Pin.Revision}
	}
	if !main {
		return nil, nil, refuse(req, r.Field, "missing_content", "skill package requires SKILL.md")
	}
	return entries, diags, nil
}
func bind(req Request, out *Result, rows []layout.Row) error {
	seen := map[string]bool{}
	for _, r := range rows {
		for name, root := range r.Locator.Env {
			value := req.Roots[root]
			if value == "" {
				return refuse(req, r.Field, "missing_root", "environment binding requires an explicit root")
			}
			// Collision checks defend the binder if future authored locators share
			// an environment name across distinct roots. Current rows agree.
			if old, exists := out.Binding.Environment[name]; exists && old != value {
				return refuse(req, r.Field, "binding_collision", "environment roots disagree")
			}
			out.Binding.Environment[name] = value
		}
		if root := r.Locator.CWD; root != "" {
			value := req.Roots[root]
			if value == "" {
				return refuse(req, r.Field, "missing_root", "cwd binding requires an explicit root")
			}
			if out.Binding.CWD != "" && out.Binding.CWD != value {
				return refuse(req, r.Field, "binding_collision", "cwd roots disagree")
			}
			out.Binding.CWD = value
		}
		for i := 0; i < len(r.Locator.Argv); i++ {
			tok := r.Locator.Argv[i]
			group := []string{tok}
			if strings.HasPrefix(tok, "--") && i+1 < len(r.Locator.Argv) && !strings.HasPrefix(r.Locator.Argv[i+1], "--") {
				i++
				group = append(group, r.Locator.Argv[i])
			}
			for j := range group {
				value, err := token(req, r, group[j])
				if err != nil {
					return err
				}
				group[j] = value
			}
			id := strings.Join(group, "\x00")
			if !seen[id] {
				out.Binding.Argv = append(out.Binding.Argv, group...)
				seen[id] = true
			}
		}
		out.Binding.BeforeResume = out.Binding.BeforeResume || r.Locator.BeforeResume
		if r.Locator.RPCProject != "" {
			project := req.Roots[layout.RootProject]
			if project == "" {
				return refuse(req, r.Field, "missing_root", "RPC project requires an explicit root")
			}
			switch r.Locator.RPCProject {
			case "thread.cwd":
				out.Binding.RPCProject["thread.cwd"] = project
			case "directory/agent":
				out.Binding.RPCProject["directory"] = project
				out.Binding.RPCProject["agent"] = req.Agent
			default:
				return refuse(req, r.Field, "invalid_binding", "unknown RPC project locator")
			}
		}
	}
	out.Binding.Environment = maps.Clone(out.Binding.Environment)
	return nil
}
func token(req Request, r layout.Row, value string) (string, error) {
	root := layout.Root("")
	switch value {
	case "{B}":
		root = layout.RootBoot
	case "{P}":
		root = layout.RootProject
	case "{H}":
		root = layout.RootHome
	case "{path}":
		base := req.Roots[r.Root]
		if base == "" {
			return "", refuse(req, r.Field, "missing_root", "file locator requires an explicit root")
		}
		return path.Join(base, r.Path), nil
	case "{agent}":
		return req.Agent, nil
	default:
		return value, nil
	}
	if req.Roots[root] == "" {
		return "", refuse(req, r.Field, "missing_root", "argv binding requires an explicit root")
	}
	return req.Roots[root], nil
}

// Providers mint their immutable handles inside the adapters internal boundary.
// Workspace can receive these handles but cannot issue or spoof an owner.
func serializerOwner(provider runtimes.ID) contract.Owner {
	switch provider {
	case runtimes.Claude:
		return claude.Owner()
	case runtimes.Codex:
		return codex.Owner()
	case runtimes.OpenCode:
		return opencode.Owner()
	case runtimes.Antigravity:
		return agy.Owner()
	}
	return contract.Owner{}
}
func reservedPaths(rows []layout.Row) []string {
	out := []string{".materialize"}
	for _, row := range rows {
		if row.Path == "" {
			continue
		}
		rel := row.Path
		if i := strings.IndexByte(rel, '{'); i >= 0 {
			rel = strings.TrimSuffix(rel[:i], "/")
		}
		if rel != "" {
			out = append(out, rel)
		}
	}
	return out
}

// synthesizeDirectories stops at an already-seen parent. Every explicit parent
// is also visited as an input entry, so its ancestors are still checked. Work is
// one existing-parent probe per entry plus one probe per new ancestor.
func synthesizeDirectories(req Request, entries []artifact.Entry, files map[string]artifact.Entry) (int, error) {
	steps := 0
	for _, e := range entries {
		for dir := path.Dir(e.Path); dir != "."; dir = path.Dir(dir) {
			steps++
			key := contract.FoldPath(dir)
			if old, ok := files[key]; ok {
				if old.Kind != artifact.EntryDirectory || old.Path != dir {
					return steps, refuse(req, layout.Resources, "path_collision", "file blocks a parent directory")
				}
				break
			}
			if len(files) >= MaxTreeEntries {
				return steps, refuse(req, layout.Resources, "input_limit", "expanded entry count exceeds render limit")
			}
			files[key] = artifact.Entry{Path: dir, Kind: artifact.EntryDirectory, Mode: 0755, Ownership: artifact.Ownership{EntryID: "workspace.render:" + dir, GroupID: "workspace.render:directories"}, Provenance: artifact.Provenance{Source: "workspace/render"}}
		}
	}
	return steps, nil
}
