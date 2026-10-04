// Package render assembles resolved provider inputs into a pure artifact tree,
// preparation requirements and launch bindings. It performs no I/O, catalog
// lookups, credential reads, profile composition or process launches.
//
// Installed assembly receives already-merged operator slots as typed inputs;
// it never reads existing documents or performs owned-key override. Installed
// apply owns that merge and preserves existing tokens/order. Native artifact
// provenance carries declared leaf-key ownership and reserved slots as data.
// Installed instructions retain the generated-by marker with DefinitionName as
// its identifier; the installer byte-compares that marker during drift checks.
// Installed Codex retains literal strings, sorted keys and explicit parent MCP
// tables. Boot retains its separate encoding convention. Installed apply will
// tighten native settings/config to owner-only 0600, a strictly safer mode delta
// from archived Claude settings; this pure package only declares that mode.
// The archived installed seeds supply no installed skills. Package-mode cases
// verify that table contract without claiming installed-skill seed parity.
//
// Installed apply acceptance is pinned by the archived seeds/cairn/{provider}/
// install-refresh corpus: preserve found JSON key order (Claude permissions
// precedes fixture_operator); preserve unknown operator leaves inside owned
// tables/objects (Codex fixture_operator inside mcp_servers.fixture) without
// dropping/reordering; never overwrite unowned values; reproduce check's
// owned-key merged settings comparison and normalized Codex TOML comparison.
// Pure refresh fixtures cover supplied operator leaves and ownership metadata
// only and claim no refresh byte parity; those seed bytes remain an apply gate.
//
// Native documents have one serializer owner; overlays cannot claim reserved
// native destinations. Commands, prompts and subagents are pinned resource
// registrations; skills are pinned packages. Supplied pins and artifacts must
// already have been resolved and verified by the caller.
//
// Path comparison uses Unicode simple case folding, but does not normalize
// NFC/NFD aliases. Fresh apply exclusive staging must fail closed; refresh
// canonical-equivalence checks belong to apply. Destinations are bounded to
// 255 bytes per component, 4096 bytes total, depth 64 and 4096 expanded entries.
// Control and bidirectional override/isolate characters are refused.
// Installed apply must never chmod, conflict on, or remove pre-existing user
// directories, even when this tree declares owned parents at 0755.
// Boot Codex preserves the supplied Native.Servers and server Env order;
// callers must provide stable ordering. Installed Codex and JSON encoders sort.
// Errors support errors.As: *Diagnostic for render validation/refusal,
// *plan.Diagnostic for unsupported target/table selection, and
// *nativefiles.Refusal for native inputs and composition.
// Package modes preserve declared bits, including SKILL.md, after stripping
// special bits and group/other write. A changed mode produces a named diagnostic.
// A clamp leaving zero permission bits uses an explicit 0644 minimum.
// Absent file modes use the table default, without inventing executability.
// Directories are 0755 inside a private 0700 boot root. Credential destinations
// remain link-only effects and replant preserves them; no credential bytes enter
// this package. Legacy validators stay until their callers migrate to this rule.
// Baseline launch fields outside this contract: executable and first/resumed
// protocol argv belong to runtime projection; permission argv/environment belong
// to the registry posture mapper; writer manifests, generations and reconcile
// reports belong to materialize. Render returns only table-derived launch deltas
// and posture references, with artifact ownership/provenance for the writer.
//
// Explicit posture cells (default / plan / accept-edits / yolo):
//
//	Provider     Native keys and ordered values
//	Claude       permissions.defaultMode: default / plan / acceptEdits / bypassPermissions
//	Codex        approval_policy: on-request / never / on-request / never
//	             sandbox_mode: read-only / read-only / workspace-write / danger-full-access
//	OpenCode     permission.edit: ask / deny / allow / allow
//	             permission.bash: ask / ask / ask / allow
//	             yolo also allows webfetch, external_directory and doom_loop
//	Antigravity  none for every posture; native_permission_omitted names the mode
//
// Every Claude cell is backed by the native settings vocabulary and equivalent
// CLI backstop in adapters/provider/bootdir_claude.go. Every Codex cell uses the
// accepted native vocabulary in adapters/provider/bootdir_codex.go and the
// measured per-posture overrides in adapters/registry/posture.go. Every OpenCode
// cell matches that file's OPENCODE_PERMISSION config object, with its native
// permission schema documented at https://opencode.ai/docs/permissions/.
// Antigravity has no evidenced native permission document; the registry supplies
// its runtime mapping. Native and runtime cells are tested for equal policy and
// independent approval/access strictness. Yolo skips deny rules; it makes no
// enforcement claim. Caller profile binding must reject required deny enforcement.
//
// Posture-reference and host-slot requests are separate: mixing native host
// permission settings with a posture reference refuses. Explicit postures add
// Claude defaultMode, derive Codex approval/sandbox, and add OpenCode permission
// objects; these replace legacy reliance on runtime overrides alone. Absent
// Codex posture emits no implicit never/workspace-write header; headless modes
// refuse without a bound posture or host-declared default. Interactive pty
// diagnoses absence and makes no headless safety claim. A host-declared native default can explicitly restore it.
// Installed definition identifiers are narrowed to safe single components;
// legacy markers quoted arbitrary names. Installed TOML normalization treats
// an empty-leaf table separator and inline-table spacing as cosmetic.
// Claude table argv puts --add-dir before --mcp-config; both flags are variadic,
// so launch composition must use -- or avoid a following positional prompt.
// OpenCode serve table tokens omit --port 0; runtime projection owns that flag.
// Agent-index/skill-mirror removals are relative to the legacy table; archived
// writer outputs contain neither. Render rebuilds provenance and file digests
// for all supplied entries and clamps overlay modes with named diagnostics.
// Instructions drop synthetic titles, front matter (except OpenCode's native
// primary-agent header), and MCP endpoint prose as listed in the table package.
package render

import (
	"fmt"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"io/fs"

	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	codex "github.com/hollis-labs/substrate/harness/adapters/codex/nativefiles"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	opencode "github.com/hollis-labs/substrate/harness/adapters/opencode/nativefiles"
	"github.com/hollis-labs/substrate/harness/workspace/materialize/artifact"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// Pin identifies a frozen content registration. Render never resolves it.
type Pin struct{ Source, Revision string }
type Content struct {
	Pin     Pin
	Body    []byte
	Package artifact.Tree
}
type Input struct {
	Resolved layout.Resolution
	Content  Content
}
type NativeInputs struct {
	// OperatorKeyPaths annotates unowned leaves in already-merged typed inputs.
	// This is metadata only; render never reads or merges an existing document.
	OperatorKeyPaths [][]string
	Servers          []contract.Server
	Claude           claude.SettingsInput
	Codex            codex.ConfigInput
	OpenCode         opencode.ConfigInput
}
type CredentialAvailability string

const (
	CredentialAvailable CredentialAvailability = "available"
	CredentialMissing   CredentialAvailability = "missing"
	CredentialDenied    CredentialAvailability = "denied"
)

type Request struct {
	// InstructionPointer explicitly selects Claude boot's @AGENTS.md option.
	// A resolved NeutralInstructions row supplies the body; no implicit mirror.
	InstructionPointer bool
	Provider           runtimes.ID
	Layer              layout.Layer
	Mode               runtimes.Mode
	Variant            layout.Variant
	// Agent is required only by a selected row path containing its placeholder.
	Agent string
	// DefinitionName supplies the installed marker identifier; no profile lookup.
	DefinitionName string
	Roots          map[layout.Root]string
	Inputs         []Input
	Native         NativeInputs
	Overlays       []artifact.Entry
	Credentials    CredentialAvailability
}

// Binding contains table-derived launch deltas, not an executable or transport
// protocol. The caller composes these tokens with its first/resumed turn inputs.
// BeforeResume requires placement before a resume subcommand where applicable.
type Binding struct {
	// Argv contains table tokens only; Claude variadic flags require a delimiter or no following positional prompt.
	Argv []string `json:"argv,omitempty"`
	// Environment contains concrete root deltas, never runtime posture env.
	Environment map[string]string `json:"environment,omitempty"`
	// CWD is the concrete table-selected directory.
	CWD string `json:"cwd,omitempty"`
	// RPCProject contains table-derived project parameters for caller RPC composition.
	RPCProject map[string]string `json:"rpc_project,omitempty"`
	// BeforeResume requires Argv placement before the resume subcommand.
	BeforeResume bool `json:"before_resume,omitempty"`
	// Posture is the detached binding reference, not permission flags.
	Posture *layout.PostureReference `json:"posture,omitempty"`
}
type Preparation struct {
	Provider    runtimes.ID
	Kind        PreparationKind
	Destination string
	Policy      layout.CredentialPolicy
}

// Result is a detached render result for the recorded target and concrete roots.
// It is valid only for Request.Roots: bindings already embed those paths.
// Entry JSON is a transport representation, never a freeze/digest encoding;
// omitempty drops an empty file's bytes. Consumers must retain explicit emptiness.
type Result struct {
	// Provider identifies the canonical runtime provider.
	Provider runtimes.ID
	// Layer records boot versus installed independently of Root.
	Layer layout.Layer
	// Mode records the selected transport or installation operation.
	Mode runtimes.Mode
	// Variant records the discovery-changing selection.
	Variant layout.Variant
	// Tree contains resolved, normalized entries with render-minted ownership, provenance and SHA-256 file digests.
	Tree artifact.Tree
	// Root identifies the logical destination root supplied in Request.Roots.
	Root layout.Root
	// RootMode is 0700 for a fresh boot root; installed 0 means do not change the existing root mode.
	RootMode fs.FileMode
	// Binding contains only table-derived deltas and the bound posture reference.
	Binding Binding
	// Effects are detached credential link-only rows; never managed file writes. Replant preserves credentials.
	Effects []layout.Row
	// Preparations describe credential availability or authorized link provisioning required before apply.
	Preparations []Preparation
	// Diagnostics report informational changes and optional omissions. Refusals return an error and zero Result.
	Diagnostics []Diagnostic
}

// Diagnostic contains no file bytes, credential values or concrete root paths.
// Mode changes identify the relative entry and both exact modes.
type Diagnostic struct {
	Posture           permission.Mode
	Code              DiagnosticCode
	Class             DiagnosticClass
	Provider          runtimes.ID
	Mode              runtimes.Mode
	Concern           layout.Field
	Reason            string
	Entry             string
	Declared, Applied fs.FileMode
}

func (d *Diagnostic) Error() string {
	return fmt.Sprintf("%s: provider=%s mode=%s concern=%s: %s", d.Code, d.Provider, d.Mode, d.Concern, d.Reason)
}
func refuse(req Request, field layout.Field, code, reason string) error {
	return &Diagnostic{Code: DiagnosticCode(code), Class: ClassRefusal, Provider: req.Provider, Mode: req.Mode, Concern: field, Reason: reason}
}

// PreparationKind is a closed preparation vocabulary.
type PreparationKind string

const (
	PreparationCredentialAvailability PreparationKind = "credential-availability"
	PreparationCredentialLink         PreparationKind = "credential-link"
)

// DiagnosticClass distinguishes information, optional omissions and failures.
type DiagnosticClass string

const (
	ClassInformational DiagnosticClass = "informational"
	ClassOmission      DiagnosticClass = "omission"
	ClassRefusal       DiagnosticClass = "refusal"
)

// DiagnosticCode identifies a render condition without parsing error prose.
// Optional table omissions retain the plan diagnostic code.
type DiagnosticCode string

const CodeClampedPackageMode DiagnosticCode = "clamped_package_mode"

const (
	CodeAgentMismatch                     DiagnosticCode = "agent_mismatch"
	CodeBindingCollision                  DiagnosticCode = "binding_collision"
	CodeClampedOverlayMode                DiagnosticCode = "clamped_overlay_mode"
	CodeCredentialWrite                   DiagnosticCode = "credential_write"
	CodeDeniedCredentials                 DiagnosticCode = "denied_credentials"
	CodeDuplicateInput                    DiagnosticCode = "duplicate_input"
	CodeHostPolicyMixture                 DiagnosticCode = "host_policy_mixture"
	CodeInputLimit                        DiagnosticCode = "input_limit"
	CodeInvalidArtifact                   DiagnosticCode = "invalid_artifact"
	CodeInvalidBinding                    DiagnosticCode = "invalid_binding"
	CodeInvalidComponent                  DiagnosticCode = "invalid_component"
	CodeInvalidCredentials                DiagnosticCode = "invalid_credentials"
	CodeInvalidDefinitionName             DiagnosticCode = "invalid_definition_name"
	CodeInvalidEffect                     DiagnosticCode = "invalid_effect"
	CodeInvalidEncoding                   DiagnosticCode = "invalid_encoding"
	CodeInvalidEntryType                  DiagnosticCode = "invalid_entry_type"
	CodeInvalidInput                      DiagnosticCode = "invalid_input"
	CodeInvalidNativeDocument             DiagnosticCode = "invalid_native_document"
	CodeInvalidOwnership                  DiagnosticCode = "invalid_ownership"
	CodeInvalidPin                        DiagnosticCode = "invalid_pin"
	CodeInvalidPointer                    DiagnosticCode = "invalid_pointer"
	CodeInvalidPosture                    DiagnosticCode = "invalid_posture"
	CodeInvalidResolution                 DiagnosticCode = "invalid_resolution"
	CodeMissingContent                    DiagnosticCode = "missing_content"
	CodeMissingCredentials                DiagnosticCode = "missing_credentials"
	CodeMissingRoot                       DiagnosticCode = "missing_root"
	CodeNativePermissionOmitted           DiagnosticCode = "native_permission_omitted"
	CodeNormalizedOverlayDirectoryMode    DiagnosticCode = "normalized_overlay_directory_mode"
	CodeNormalizedPackageDirectoryMode    DiagnosticCode = "normalized_package_directory_mode"
	CodeOmittedEmptyInstalledInstructions DiagnosticCode = "omitted_empty_installed_instructions"
	CodePathCollision                     DiagnosticCode = "path_collision"
	CodePointerBody                       DiagnosticCode = "pointer_body"
	CodePostureAbsent                     DiagnosticCode = "posture_absent"
	CodePostureRequired                   DiagnosticCode = "posture_required"
	CodeProviderInputMismatch             DiagnosticCode = "provider_input_mismatch"
	CodeReservedDestination               DiagnosticCode = "reserved_destination"
	CodeUnpinnedContent                   DiagnosticCode = "unpinned_content"
	CodeUnresolvedContent                 DiagnosticCode = "unresolved_content"
	CodeUnresolvedInput                   DiagnosticCode = "unresolved_input"
	CodeUnsafePackage                     DiagnosticCode = "unsafe_package"
	CodeUnsafePath                        DiagnosticCode = "unsafe_path"
	CodeUnsafeRoot                        DiagnosticCode = "unsafe_root"
	CodeUnsupportedRenderer               DiagnosticCode = "unsupported_renderer"
)
