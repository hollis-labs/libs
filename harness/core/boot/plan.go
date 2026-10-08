package boot

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/adapters/registry"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// Plan consumes finished content and explicit snapshots. It calls no Ports,
// hook, environment lookup, clock, filesystem operation or process detector.
// Even a refused plan retains a detached description of supplied provenance.
func Plan(in Input, host HostInputs) (Planned, error) {
	p := Planned{description: initialDescription(in, host)}
	frozen, err := detach(in)
	if err != nil {
		return p, failure(PhasePlan, "invalid_input", err)
	}
	// Artifact's omitempty bytes distinguish absent content from an empty
	// file. Preserve that distinction with its recursive clone contract.
	for i := range frozen.Context.Artifacts {
		frozen.Context.Artifacts[i].Content.Package.Entries = artifact.CloneEntries(in.Context.Artifacts[i].Content.Package.Entries)
	}
	in = frozen
	data, err := detach(host.HostInputDTO)
	if err != nil {
		return p, failure(PhasePlan, "invalid_host_input", err)
	}
	host.HostInputDTO = data
	p.description = initialDescription(in, host)
	if err := validateInput(in, host); err != nil {
		return p, err
	}
	if len(in.Definition.Policy.Approvals) > 0 || len(in.Definition.Policy.Escalation) > 0 {
		return p, failure(PhasePlan, "unsupported_policy_references", errors.New("resolved approvals and escalation have no supported boot implementation"))
	}
	ceiling := host.Ceiling
	ceiling.RequiresDenyEnforcement = ceiling.RequiresDenyEnforcement || in.Definition.Policy.RequiresDenyEnforcement
	profile, err := permission.BindProfile(in.Definition.Policy.Profile.Name, ceiling)
	if err != nil {
		return p, failure(PhasePlan, errorCode(err, "permission_binding"), err)
	}
	if profile != in.Definition.Policy.Profile {
		return p, failure(PhasePlan, "policy_binding_mismatch", errors.New("resolved profile does not match the authored binding"))
	}
	spec := in.Workspace
	if spec.Sandbox.Profile != (permission.ProfileBinding{}) && spec.Sandbox.Profile != profile {
		return p, failure(PhasePlan, "policy_binding_mismatch", errors.New("workspace profile differs from resolved definition"))
	}
	if !reflect.ValueOf(spec.Sandbox.Policy).IsZero() && !reflect.DeepEqual(spec.Sandbox.Policy, in.Definition.Policy.Restrictions) {
		return p, failure(PhasePlan, "restriction_mismatch", errors.New("workspace restrictions differ from resolved definition"))
	}
	spec.Sandbox.Profile, spec.Sandbox.Policy = profile, in.Definition.Policy.Restrictions
	spec.EffectInputs = host.Effects
	for _, hook := range in.Hooks {
		if hook.Required {
			return p, failure(PhasePlan, "unsupported_required_hook", fmt.Errorf("event %q matcher %q: boot does not install or execute hooks", hook.Event, hook.Matcher))
		}
		p.description.Diagnostics = append(p.description.Diagnostics, Diagnostic{"optional_hook_omitted", "boot does not install or execute hooks: " + hook.Event})
	}
	project, err := projectPath(spec, host.Resources)
	if err != nil {
		return p, failure(PhasePlan, "unresolved_cwd", err)
	}
	roots := map[plan.Root]string{plan.RootBoot: spec.Boot.Candidate.Path, plan.RootHome: spec.Home.Root.Path, plan.RootProject: project}
	request := render.Request{Provider: in.Dispatch.Provider, Layer: plan.Boot, Mode: in.Dispatch.Mode, Variant: in.Dispatch.Variant, Agent: in.Definition.Name, DefinitionName: in.Definition.Name, Roots: roots, Credentials: host.CredentialAvailability, Native: render.NativeInputs{Servers: host.Servers}}
	lookup := func(id runtimes.ID, posture permission.Mode, mode runtimes.Mode) error {
		d, ok := registry.Lookup(string(id))
		if !ok {
			return errors.New("unknown provider")
		}
		_, err := d.PostureFor(posture, mode)
		return err
	}
	resolve := func(field plan.Field, requirement plan.Requirement, components map[string]string, content render.Content) error {
		if components == nil {
			components = map[string]string{"agent": in.Definition.Name}
		}
		r, err := plan.Resolve(plan.Request{Key: plan.Key{Provider: in.Dispatch.Provider, Layer: plan.Boot, Mode: in.Dispatch.Mode, Variant: in.Dispatch.Variant, Field: field}, Requirement: requirement, Components: components, Posture: profile.Mode, LookupPosture: lookup})
		if err != nil {
			return err
		}
		request.Inputs = append(request.Inputs, render.Input{Resolved: r, Content: content})
		return nil
	}
	// Policy has one owner. Settings compose with it through the canonical
	// renderer. The native document never supplies an effective permission.
	if err := resolve(plan.Permissions, plan.Required, nil, render.Content{}); err != nil {
		return p, failure(PhasePlan, "unsupported_policy", err)
	}
	if in.Dispatch.Provider != runtimes.Antigravity {
		if err := resolve(plan.Settings, plan.Required, nil, render.Content{}); err != nil {
			return p, failure(PhasePlan, "unsupported_settings", err)
		}
	}
	if in.Dispatch.Provider == runtimes.Antigravity {
		if err := resolve(plan.PlantingPlugin, plan.Required, nil, render.Content{}); err != nil {
			return p, failure(PhasePlan, "unsupported_plugin", err)
		}
	}
	// Launch conventions refer to this native MCP document even when the
	// explicitly supplied server set is empty. Projection files are not used.
	if err := resolve(plan.MCP, plan.Required, nil, render.Content{}); err != nil {
		return p, failure(PhasePlan, "unsupported_mcp", err)
	}
	for _, item := range in.Context.Artifacts {
		switch item.Field {
		case plan.Permissions, plan.Settings, plan.MCP, plan.Hooks, plan.Credentials, plan.PlantingPlugin:
			return p, failure(PhasePlan, "reserved_context_field", fmt.Errorf("%s belongs to resolved policy or host bindings", item.Field))
		}
		if !text(item.Content.Pin.Source) || !text(item.Content.Pin.Revision) {
			return p, failure(PhasePlan, "unpinned_context", errors.New("context artifacts require source and revision pins"))
		}
		if err := resolve(item.Field, item.Requirement, item.Components, item.Content); err != nil {
			return p, failure(PhasePlan, "unsupported_context", err)
		}
	}
	if len(spec.Credentials) > 0 {
		if err := resolve(plan.Credentials, plan.Required, nil, render.Content{}); err != nil {
			return p, failure(PhasePlan, "unsupported_credentials", err)
		}
	}
	if err := nativeSettings(in, &request.Native); err != nil {
		return p, err
	}
	rendered, err := render.Render(request)
	if err != nil {
		return p, failure(PhasePlan, "render_refused", err)
	}
	wp, err := workspace.Plan(spec, workspace.ResolvedContent{Rendered: []render.Result{rendered}, Roots: roots}, host.Resources, host.Observations)
	if err != nil {
		return p, failure(PhasePlan, errorCode(err, "workspace_refused"), err)
	}
	p.description.Bindings = wp.Bindings()
	for _, d := range rendered.Diagnostics {
		p.description.Diagnostics = append(p.description.Diagnostics, Diagnostic{string(d.Code), d.Reason})
	}
	if row, err := plan.Find(plan.Key{Provider: in.Dispatch.Provider, Layer: plan.Boot, Mode: in.Dispatch.Mode, Variant: in.Dispatch.Variant, Field: plan.Settings}); err == nil && row.Path != "" {
		p.description.SettingsPath = filepath.Join(spec.Boot.Candidate.Path, row.Path)
	}
	if err := describeProcess(in, host, project, rendered.Tree, &p.description); err != nil {
		return p, failure(PhaseProject, errorCode(err, "process_projection"), err)
	}
	p.workspace = wp
	return p, nil
}

func validateInput(in Input, host HostInputs) error {
	refuse := func(code, reason string) error { return failure(PhasePlan, code, errors.New(reason)) }
	if in.SchemaVersion != SchemaVersion {
		return refuse("unsupported_schema", "unsupported boot input schema")
	}
	for _, value := range []string{string(in.Dispatch.Provider), string(in.Dispatch.Mode), in.Dispatch.Model, in.Dispatch.Effort} {
		if !text(value) {
			return refuse("missing_dispatch_selection", "provider, mode, model and effort must be explicit")
		}
	}
	if strings.HasPrefix(in.Dispatch.Model, "-") || strings.HasPrefix(in.Dispatch.Effort, "-") || strings.HasPrefix(in.Dispatch.ResumeID, "-") || strings.ContainsAny(in.Dispatch.ResumeID, "\x00\r\n") || strings.ContainsRune(in.Dispatch.Prompt, 0) || strings.ContainsRune(in.Dispatch.SystemPrompt, 0) {
		return refuse("invalid_dispatch_value", "option values must not become options or contain process-incompatible bytes")
	}
	if !text(in.Definition.Name) || !text(in.Definition.Revision) || in.Definition.Revision != in.Workspace.Identity.DefinitionRevision {
		return refuse("definition_revision_mismatch", "resolved definition must bind the workspace revision")
	}
	for _, origin := range []Provenance{in.Definition.Provenance, in.Definition.Policy.Provenance, in.Dispatch.Provenance, in.Context.Provenance, host.Provenance} {
		if !validOrigin(origin) {
			return refuse("missing_provenance", "resolved inputs require explicit source and revision")
		}
	}
	if in.Settings.Claude != nil && !validOrigin(in.Settings.Provenance) {
		return refuse("missing_provenance", "native settings require explicit source and revision")
	}
	for _, hook := range in.Hooks {
		if !text(hook.Event) || !text(hook.Command) || !validOrigin(hook.Provenance) {
			return refuse("unresolved_hook", "hook binding requires event, command and provenance")
		}
	}
	if in.Workspace.Operation != workspace.Prepare && in.Workspace.Operation != workspace.Resume || in.Workspace.Installed != nil || in.Workspace.Publication != nil {
		return refuse("unsupported_operation", "boot prepares an inactive candidate only")
	}
	if !reflect.ValueOf(in.Workspace.EffectInputs).IsZero() {
		return refuse("host_effect_inputs_required", "effect attestations belong exclusively to HostInputs")
	}
	return nil
}

func nativeSettings(in Input, out *render.NativeInputs) error {
	// This is the boot package's evidenced serialization set, not a model
	// capability claim. Unknown/custom values require a separately supported
	// binding; they must not reach settings or argv as arbitrary strings.
	var supported []string
	switch in.Dispatch.Provider {
	case runtimes.Claude:
		// The effortLevel settings key excludes the session-only max level.
		supported = []string{"low", "medium", "high", "xhigh"}
	case runtimes.Codex:
		supported = []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	case runtimes.Antigravity:
		supported = []string{"low", "medium", "high"}
	}
	if supported != nil {
		found := false
		for _, effort := range supported {
			if effort == in.Dispatch.Effort {
				found = true
				break
			}
		}
		if !found {
			return failure(PhasePlan, "unsupported_effort", errors.New("explicit effort has no supported provider binding"))
		}
	}
	if in.Settings.Claude != nil && in.Dispatch.Provider != runtimes.Claude {
		return failure(PhasePlan, "provider_settings_mismatch", errors.New("Claude settings supplied for another provider"))
	}
	switch in.Dispatch.Provider {
	case runtimes.Claude:
		out.Claude.Slots = append(out.Claude.Slots, nativefiles.Slot{Key: "effortLevel", Value: in.Dispatch.Effort})
		if c := in.Settings.Claude; c != nil {
			if c.PermissionsDefaultMode != "" {
				return failure(PhasePlan, "native_policy_mixture", errors.New("native defaultMode is not authority and cannot replace the bound permission profile"))
			}
			if c.EffortLevel != "" && c.EffortLevel != in.Dispatch.Effort {
				return failure(PhasePlan, "effort_mismatch", errors.New("native effort differs from explicit dispatch"))
			}
			if c.TUI != "" {
				if c.TUI != "fullscreen" {
					return failure(PhasePlan, "unsupported_tui", errors.New("unknown resolved Claude TUI value"))
				}
				out.Claude.Slots = append(out.Claude.Slots, nativefiles.Slot{Key: "tui", Value: c.TUI})
			}
			for _, b := range []struct {
				key   string
				value *bool
			}{{"awaySummaryEnabled", c.AwaySummaryEnabled}, {"skipDangerousModePermissionPrompt", c.SkipDangerousModePermissionPrompt}, {"skipAutoPermissionPrompt", c.SkipAutoPermissionPrompt}} {
				if b.value != nil {
					out.Claude.Slots = append(out.Claude.Slots, nativefiles.Slot{Key: b.key, Value: *b.value})
				}
			}
			if c.EnabledPlugins != nil {
				out.Claude.Slots = append(out.Claude.Slots, nativefiles.Slot{Key: "enabledPlugins", Value: c.EnabledPlugins})
			}
			if c.Environment != nil {
				out.Claude.Slots = append(out.Claude.Slots, nativefiles.Slot{Key: "env", Value: c.Environment})
			}
		}
	case runtimes.Codex:
		out.Codex.Slots = []nativefiles.Slot{{Key: "model_reasoning_effort", Value: in.Dispatch.Effort}}
	case runtimes.Antigravity:
		// Its existing launch convention carries the explicit effort argument.
	case runtimes.OpenCode:
		return failure(PhasePlan, "unsupported_effort", errors.New("no evidenced OpenCode effort binding in the existing adapter"))
	default:
		return failure(PhasePlan, "unsupported_provider", errors.New("no native settings contract"))
	}
	return nil
}

func projectPath(spec workspace.Spec, resources workspace.Resources) (string, error) {
	var root *workspace.RootRef
	for i := range resources.Roots {
		if resources.Roots[i].ID == spec.CWD.RootID {
			if root != nil {
				return "", errors.New("ambiguous CWD resource")
			}
			root = &resources.Roots[i]
		}
	}
	if root == nil {
		return "", errors.New("CWD request lacks a host-resolved root")
	}
	cwd := filepath.Join(root.Path, spec.CWD.Relative)
	if spec.CWD.ProtocolProject != "" {
		cwd = spec.CWD.ProtocolProject
	} else if spec.CWD.Child != "" {
		cwd = spec.CWD.Child
	}
	return cwd, nil
}

func initialDescription(in Input, host HostInputs) Description {
	d := Description{BootDir: in.Workspace.Boot.Candidate.Path, Provider: in.Dispatch.Provider, Mode: in.Dispatch.Mode, Model: in.Dispatch.Model, Effort: in.Dispatch.Effort, Provenance: Origins{Definition: in.Definition.Provenance, Policy: in.Definition.Policy.Provenance, Dispatch: in.Dispatch.Provenance, Context: in.Context.Provenance, Settings: in.Settings.Provenance, Host: host.Provenance}}
	for _, h := range in.Hooks {
		d.Provenance.Hooks = append(d.Provenance.Hooks, h.Provenance)
	}
	d.Resources, _ = detach(host.Resources)
	d.Trust, _ = detach(in.Workspace.Trust)
	d.Definition, _ = detach(in.Definition)
	d.RequestedSettings, _ = detach(in.Settings)
	d.RequestedHooks, _ = detach(in.Hooks)
	d.Environment, _ = detach(host.Environment)
	return d
}

func (p Planned) Description() Description { out, _ := detach(p.description); return out }

func detach[T any](in T) (T, error) {
	var out T
	if !validEncoding(reflect.ValueOf(in)) {
		return out, errors.New("invalid UTF-8 in resolved input; normalization is forbidden")
	}
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

func validEncoding(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Interface, reflect.Pointer:
		return v.IsNil() || validEncoding(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" && !validEncoding(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return true
		}
		for i := 0; i < v.Len(); i++ {
			if !validEncoding(v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if !validEncoding(key) || !validEncoding(v.MapIndex(key)) {
				return false
			}
		}
	}
	return true
}
func validOrigin(p Provenance) bool { return text(p.Source) && text(p.Revision) }
func text(s string) bool {
	return s != "" && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func failure(phase Phase, code string, err error) *Error { return &Error{phase, code, err} }
func errorCode(err error, fallback string) string {
	var b *Error
	if errors.As(err, &b) {
		return b.Code
	}
	var w *workspace.Refusal
	if errors.As(err, &w) {
		return w.Code
	}
	var p *permission.ProfileError
	if errors.As(err, &p) {
		return p.Code
	}
	var n *nativefiles.Refusal
	if errors.As(err, &n) {
		return string(n.Code)
	}
	var l *plan.Diagnostic
	if errors.As(err, &l) {
		return l.Code
	}
	var r *render.Diagnostic
	if errors.As(err, &r) {
		return string(r.Code)
	}
	return fallback
}
