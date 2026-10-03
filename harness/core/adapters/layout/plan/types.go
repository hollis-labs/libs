package plan

import (
	"fmt"
	"maps"
	"slices"

	"github.com/hollis-labs/substrate/harness/adapters/layout"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

type Layer string

const (
	Boot      Layer = "boot"
	Installed Layer = "installed"
)

// InstallMode is an installation operation, not a process transport.
const InstallMode runtimes.Mode = "install"

// Field is a closed semantic input vocabulary, not a YAML spelling.
type Field string

const (
	Instructions        Field = "instructions"
	NeutralInstructions Field = "neutral-instructions"
	InstructionPointer  Field = "instruction-pointer"
	Permissions         Field = "permissions"
	Settings            Field = "settings"
	MCP                 Field = "mcp"
	Skills              Field = "skills"
	Commands            Field = "commands"
	Subagents           Field = "subagents"
	Prompts             Field = "prompts"
	Credentials         Field = "credentials"
	Resources           Field = "resources"
	Hooks               Field = "hooks"
	PlantingPlugin      Field = "planting-plugin"
	Kickoff             Field = "kickoff"
)

// Sources maps semantic inputs to the closed agentdef v2 content channels once.
// Empty means host input. Content registrations use pinned resources, not fields
// added to agentdef. Returned slices can be changed by the caller.
func Sources(f Field) ([]string, bool) {
	s, ok := fieldSources[f]
	return slices.Clone(s), ok
}

var fieldSources = map[Field][]string{
	Instructions:        {"behavior.instructions", "behavior.sops", "body", "harness_profile.steering"},
	NeutralInstructions: {"behavior.instructions", "behavior.sops", "body"},
	InstructionPointer:  {"behavior.instructions", "harness_profile.steering"},
	Permissions:         {"harness_profile.permissions.profile", "harness_profile.approvals", "harness_profile.escalation"},
	Settings:            nil, MCP: {"requirements.tools"}, Skills: {"requirements.skills"},
	Commands: {"requirements.resources"}, Subagents: {"requirements.resources"}, Prompts: {"requirements.resources"},
	Credentials: nil, Resources: {"requirements.resources"}, Hooks: {"behavior.hooks", "harness_profile.approvals"},
	PlantingPlugin: nil, Kickoff: {"harness_profile.context.sources", "harness_profile.context.policy"},
}

type Form string

const (
	File           Form = "file"
	Package        Form = "skill-package"
	Slot           Form = "document-slot"
	Link           Form = "link-effect"
	Resource       Form = "resource-request"
	RuntimeBinding Form = "runtime-binding"
)

type Capability string

const (
	Supported   Capability = "supported"
	Unsupported Capability = "unsupported"
	Unmeasured  Capability = "unmeasured"
)
const (
	FileMode      uint32 = 0644
	DirectoryMode uint32 = 0755
	BootRootMode  uint32 = 0700
)

// Locator carries tokens only. B/P/H and {path}/{agent} remain typed templates
// for the launch binder. RPC project parameters must never become spawn flags.
type Locator struct {
	Argv         []string               `json:"argv,omitempty"`
	Env          map[string]layout.Root `json:"env,omitempty"`
	CWD          layout.Root            `json:"cwd,omitempty"`
	RPCProject   string                 `json:"rpc_project,omitempty"`
	BeforeResume bool                   `json:"before_resume,omitempty"`
}

// PostureReference names the existing runtime mapper, without owning its argv/env.
// PostureID is supplied explicitly after D4 binding; empty means absent.
type PostureReference struct {
	Provider  runtimes.ID
	Mapper    string
	PostureID string
}

type Evidence struct {
	Reference    string   `json:"reference"`
	Observations []string `json:"observations,omitempty"`
	Note         string   `json:"note,omitempty"`
}

// CredentialPolicy excludes credential destinations from managed file writes.
// Replant preserves existing credentials; fresh link provisioning belongs to
// the authorized workspace effect handler, which must refuse overwrite.
type CredentialPolicy string

const LinkOnlyNeverWrite CredentialPolicy = "link-only-never-write"

type Row struct {
	CredentialPolicy CredentialPolicy  `json:"credential_policy,omitempty"`
	Posture          *PostureReference `json:"posture,omitempty"`
	Provider         runtimes.ID       `json:"provider"`
	Layer            Layer             `json:"layer"`
	Mode             runtimes.Mode     `json:"mode,omitempty"`
	Variant          layout.Variant    `json:"variant,omitempty"`
	Field            Field             `json:"field"`
	Concern          string            `json:"concern"`
	Root             layout.Root       `json:"root"`
	Path             string            `json:"path,omitempty"`
	Form             Form              `json:"form"`
	ModeBits         uint32            `json:"mode_bits"`
	Renderer         string            `json:"renderer,omitempty"`
	DocumentSlot     string            `json:"document_slot,omitempty"`
	Locator          Locator           `json:"locator"`
	Capability       Capability        `json:"capability"`
	ExclusiveMCP     Capability        `json:"exclusive_mcp,omitempty"`
	Evidence         Evidence          `json:"evidence"`
	Reason           string            `json:"reason,omitempty"`
}

func (r Row) clone() Row {
	if r.Posture != nil {
		p := *r.Posture
		r.Posture = &p
	}
	r.Locator.Argv = slices.Clone(r.Locator.Argv)
	r.Locator.Env = maps.Clone(r.Locator.Env)
	r.Evidence.Observations = slices.Clone(r.Evidence.Observations)
	return r
}

type Key struct {
	Provider runtimes.ID
	Layer    Layer
	Mode     runtimes.Mode
	Variant  layout.Variant
	Field    Field
}

// Diagnostic is both a typed refusal and an optional omission. It contains no
// credential bytes or concrete root paths.
type Diagnostic struct {
	Code     string
	Provider runtimes.ID
	Layer    Layer
	Mode     runtimes.Mode
	Concern  Field
	Reason   string
}

func (d *Diagnostic) Error() string {
	return fmt.Sprintf("%s: provider=%s layer=%s mode=%s concern=%s: %s", d.Code, d.Provider, d.Layer, d.Mode, d.Concern, d.Reason)
}
func diagnostic(k Key, code, reason string) *Diagnostic {
	return &Diagnostic{code, k.Provider, k.Layer, k.Mode, k.Field, reason}
}

// NormalizeProvider is called once by the plan compiler. Lookup requires the
// canonical ID and deliberately does not normalize again.
func NormalizeProvider(id runtimes.ID) runtimes.ID {
	switch id {
	case "agy":
		return runtimes.Antigravity
	case "claude-code", "claudecode":
		return runtimes.Claude
	case "open-code":
		return runtimes.OpenCode
	}
	return id
}
