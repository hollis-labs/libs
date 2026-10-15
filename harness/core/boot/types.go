package boot

import (
	"fmt"

	"github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	"github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/adapters/provider"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/harness/sandbox"
	"github.com/hollis-labs/substrate/harness/workspace"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

const SchemaVersion = "harness.boot.v1"

// Provenance is supplied by the owner that resolved this value. It is evidence
// about input origin, never an authorization or a native capability.
type Provenance struct {
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

type Policy struct {
	Approvals    []PolicyReference            `json:"approvals,omitempty"`
	Escalation   []PolicyReference            `json:"escalation,omitempty"`
	Profile      permission.ProfileBinding    `json:"profile"`
	Restrictions sandbox.ResolvedAccessPolicy `json:"restrictions"`
	// Required deny enforcement excludes bypass profiles; it does not assert
	// that a native runtime implements the requested restrictions.
	RequiresDenyEnforcement bool       `json:"requires_deny_enforcement"`
	Provenance              Provenance `json:"provenance"`
}

// PolicyReference preserves a caller-resolved opaque URI and content SHA256.
// It is not permission to execute an approval or escalation implementation.
type PolicyReference struct {
	URI    string `json:"uri"`
	Digest string `json:"digest"`
}

type Definition struct {
	Name       string     `json:"name"`
	Revision   string     `json:"revision"`
	Policy     Policy     `json:"policy"`
	Provenance Provenance `json:"provenance"`
}

// Dispatch has no catalog, definition or runtime defaults. Effort is an
// explicit native setting, separate from both model and permission policy.
type Dispatch struct {
	Provenance   Provenance    `json:"provenance"`
	Provider     runtimes.ID   `json:"provider"`
	Mode         runtimes.Mode `json:"mode"`
	Variant      plan.Variant  `json:"variant,omitempty"`
	Model        string        `json:"model"`
	Effort       string        `json:"effort"`
	Prompt       string        `json:"prompt"`
	SystemPrompt string        `json:"system_prompt,omitempty"`
	ResumeID     string        `json:"resume_id,omitempty"`
}

// ContextArtifact carries finished composition, not a callback or definition.
// Field and Components select the existing authored layout contract. Content
// pins static, dynamic and subagent inputs using the same neutral vocabulary.
type ContextArtifact struct {
	Field       plan.Field        `json:"field"`
	Requirement plan.Requirement  `json:"requirement"`
	Components  map[string]string `json:"components,omitempty"`
	Content     render.Content    `json:"content"`
}

type ContextHook struct {
	Provenance Provenance        `json:"provenance"`
	Artifacts  []ContextArtifact `json:"artifacts"`
}

// NativeSettings contains resolved settings only. None grants permissions,
// directory access or trust. Additional provider shapes can be added explicitly.
type NativeSettings struct {
	Claude     *ClaudeSettings `json:"claude,omitempty"`
	Provenance Provenance      `json:"provenance"`
}

type ClaudeSettings struct {
	PermissionsDefaultMode            string            `json:"permissions_default_mode,omitempty"`
	EffortLevel                       string            `json:"effort_level,omitempty"`
	TUI                               string            `json:"tui,omitempty"`
	AwaySummaryEnabled                *bool             `json:"away_summary_enabled,omitempty"`
	SkipDangerousModePermissionPrompt *bool             `json:"skip_dangerous_mode_permission_prompt,omitempty"`
	SkipAutoPermissionPrompt          *bool             `json:"skip_auto_permission_prompt,omitempty"`
	EnabledPlugins                    map[string]bool   `json:"enabled_plugins,omitempty"`
	Environment                       map[string]string `json:"environment,omitempty"`
}

type HookBinding struct {
	Event      string     `json:"event"`
	Matcher    string     `json:"matcher"`
	Command    string     `json:"command"`
	Required   bool       `json:"required"`
	Provenance Provenance `json:"provenance"`
}

// Input contains requests and resolved content. Workspace is a semantic
// directory/access request: it cannot mint grants. Its EffectInputs must be
// empty; only HostInputs supplies resolved host effect attestations. The boot
// package accepts Prepare/Resume, not installed or publication operations.
type Input struct {
	SchemaVersion string         `json:"schema_version"`
	Definition    Definition     `json:"definition"`
	Dispatch      Dispatch       `json:"dispatch"`
	Workspace     workspace.Spec `json:"workspace"`
	Context       ContextHook    `json:"context"`
	Settings      NativeSettings `json:"settings"`
	Hooks         []HookBinding  `json:"hooks,omitempty"`
}

// EnvironmentEntry describes an explicit host amendment, including TMPDIR,
// GOTMPDIR and TEAM_* values. Nothing is read from ambient process environment.
type EnvironmentEntry struct {
	Delta      provider.EnvDelta `json:"delta"`
	Provenance Provenance        `json:"provenance"`
}

// HostInputDTO is data-only and serializable. Decoding it does not confer
// authority. The caller must supply Ports independently for Prepare.
type HostInputDTO struct {
	CredentialAvailability render.CredentialAvailability `json:"credential_availability"`
	Resources              workspace.Resources           `json:"resources"`
	Observations           workspace.Observations        `json:"observations"`
	Effects                workspace.EffectInputs        `json:"effects"`
	Ceiling                permission.Ceiling            `json:"ceiling"`
	Servers                []nativefiles.Server          `json:"servers,omitempty"`
	Environment            []EnvironmentEntry            `json:"environment,omitempty"`
	Provenance             Provenance                    `json:"provenance"`
}

type HostInputs struct {
	HostInputDTO
	Ports workspace.Ports `json:"-"`
}

type Diagnostic struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Description describes a possible process; it is not permission to spawn it.
// Bindings are the canonical table deltas, distinct from complete Argv/Env.
type Description struct {
	Definition        Definition         `json:"definition"`
	RequestedSettings NativeSettings     `json:"requested_settings"`
	RequestedHooks    []HookBinding      `json:"requested_hooks"`
	BootDir           string             `json:"boot_dir"`
	Provider          runtimes.ID        `json:"provider"`
	Mode              runtimes.Mode      `json:"mode"`
	Model             string             `json:"model"`
	Effort            string             `json:"effort"`
	Executable        string             `json:"executable"`
	Argv              []string           `json:"argv"`
	Environment       []EnvironmentEntry `json:"environment"`
	CWD               string             `json:"cwd"`
	SettingsPath      string             `json:"settings_path,omitempty"`
	Bindings          []render.Binding   `json:"bindings"`
	// Delivery describes the first/resumed turn's transport payload. It is
	// never sent by this package; RPC payloads remain caller responsibilities.
	Delivery     Delivery                `json:"delivery"`
	Resources    workspace.Resources     `json:"resources"`
	Trust        []workspace.TrustSpec   `json:"trust"`
	TrustOutcome []effects.TrustEvidence `json:"trust_outcome"`
	Provenance   Origins                 `json:"provenance"`
	Diagnostics  []Diagnostic            `json:"diagnostics"`
}

// Origins survives refusal and partial application as detached source evidence.
type Origins struct {
	Definition Provenance   `json:"definition"`
	Policy     Provenance   `json:"policy"`
	Dispatch   Provenance   `json:"dispatch"`
	Context    Provenance   `json:"context"`
	Settings   Provenance   `json:"settings"`
	Host       Provenance   `json:"host"`
	Hooks      []Provenance `json:"hooks"`
}

type Delivery struct {
	Kind         string `json:"kind"`
	Prompt       string `json:"prompt,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	ResumeID     string `json:"resume_id,omitempty"`
	Stdin        []byte `json:"stdin,omitempty"`
}

// Planned owns a frozen engine plan and detached process description.
type Planned struct {
	workspace   workspace.PlannedWorkspace
	description Description
}

func (p Planned) Valid() bool    { return p.workspace.Valid() }
func (p Planned) Digest() string { return p.workspace.Digest() }

type Result struct {
	Description Description           `json:"description"`
	Apply       workspace.ApplyResult `json:"apply"`
}

func (r Result) ArtifactsComplete() bool             { return r.Apply.ArtifactsComplete() }
func (r Result) Obligations() []workspace.Obligation { return r.Apply.Clone().Obligations }

type Phase string

const (
	PhasePlan        Phase = "plan"
	PhaseProject     Phase = "project"
	PhaseMaterialize Phase = "materialize"
)

type Error struct {
	Phase Phase
	Code  string
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("boot %s (%s): %v", e.Phase, e.Code, e.Err) }
func (e *Error) Unwrap() error { return e.Err }
