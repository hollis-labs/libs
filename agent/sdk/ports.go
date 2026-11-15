package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hollis-labs/libs/agent/core/toolbroker/broker"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/agentdef"
	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
	"github.com/hollis-labs/libs/security/policy"
)

// CapabilityApproval requires a usable native gate and authority verifier before
// the first model effect when named in definition.Requirements.Requires.
const CapabilityApproval = "approval"

var (
	ErrInvalid       = errors.New("sdk: invalid configuration or input")
	ErrUnsupported   = errors.New("sdk: unsupported capability")
	ErrPending       = errors.New("sdk: approval pending")
	ErrLimit         = errors.New("sdk: run limit exceeded")
	ErrContextBudget = errors.New("sdk: context budget exceeded")
	ErrBusy          = errors.New("sdk: operative already running")
	ErrRunUsed       = errors.New("sdk: run ID already reserved; reconciled resume unsupported")
	ErrEffectPanic   = errors.New("sdk: admitted callback panicked; reconcile external effects")
	ErrAdmission     = errors.New("sdk: invalid host admission")
)

// Ports are injected, trusted host bindings. New snapshots configuration values;
// implementations must remain safe for their documented use throughout a Run.
type Ports struct {
	Router       *Router
	Tools        []Tool
	Broker       broker.Broker // optional; selection can only narrow Tools
	Environment  EnvironmentVerifier
	Admission    AdmissionPort
	Policy       PolicyPort
	Gate         GateAdapter // optional for policy-Allow hosts; Ask requires Gate + Authority
	Authority    gate.AuthorityVerifier
	Resources    ResourceResolver         // required only when the definition contains pins
	Memory       MemoryReader             // optional bounded retrieval, never persistence
	Sequencer    *runtimeevents.Sequencer // optional shared producer sequence; default per Operative
	Events       runtimeevents.Sink       // required synchronous observations; errors stop the run
	Capabilities []string                 // host support vocabulary, never a permission grant
	Limits       Limits
	Now          func() time.Time
}

type Limits struct {
	MaxTurns           int
	MaxOutputTokens    int
	IdleTimeout        time.Duration
	ApprovalTTL        time.Duration
	MemoryItems        int
	MemoryBytes        int
	MaxToolResultBytes int
}

// Input addresses one explicitly enrolled attempt. ID is a host-unique run ID
// within its owner/operative/conversation, across attempt replacements. Every ID
// is single-use, including after cancellation, failure or uncertainty. Each Run
// starts from Text alone. Session/history ownership is outside this API.
type Input struct {
	ID          string
	Binding     runtimecontract.Binding
	Environment runtimecontract.EnvironmentRef
	Text        string
	Route       string // empty selects the router's explicit default
}

type Status string

const (
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
	Pending   Status = "pending"
)

type Result struct {
	RunID         string
	Binding       runtimecontract.Binding
	Status        Status
	Text          string
	Turns         int
	Usage         llmtypes.Usage
	PendingTicket *gate.Ticket // can be inspected/cancelled by the host; no SDK resume
}

// EnvironmentVerifier resolves host-issued receipts, current binding, live lease,
// verified owner and capabilities. ValidateAt alone cannot implement this port.
type EnvironmentVerifier interface {
	Verify(context.Context, runtimecontract.Binding, runtimecontract.EnvironmentRef, []string) error
}

// RunReservation is the immutable host admission record for one logical input.
// InputDigest hashes the complete input, definition and selected route/model;
// no raw prompt is retained here. It is not a credential or an effect receipt.
// The host reservation key is OwnerID + OperativeID + ConversationID + RunID,
// excluding attempt/generation so replacement cannot replay uncertain work.
type RunReservation struct {
	RunID            string
	Binding          runtimecontract.Binding
	Environment      runtimecontract.EnvironmentRef
	DefinitionDigest string
	Route            string
	Model            string
	InputDigest      runtimecontract.Digest
}

// Admission identifies the effect about to start. Model is also external and
// billable. Tool admission includes the exact descriptor and gate projection.
type Admission struct {
	Run              RunReservation // exact record accepted by ReserveRun, required for every effect
	Kind             string         // model, memory, tool
	Binding          runtimecontract.Binding
	Environment      runtimecontract.EnvironmentRef
	DefinitionDigest string
	Route            string
	Operation        *Operation
	Request          *gate.Request
	Decision         *gate.Decision
	Policy           *PolicyResult
}

// AdmissionPort must verify current binding/environment/grants at admission and
// fence their changes through effect start. For tools it also rehashes the exact
// descriptor, re-evaluates/verifies Policy and grants, verifies authoritative
// approval when Request/Decision are present, and atomically claims the operation
// before invoking start synchronously exactly once. Keep
// uncertain claims consumed until reconciliation. Never retry start or return
// success without calling it. The callback must finish before Admit returns;
// the host owns cancellation of admitted work if its binding is later revoked.
type AdmissionPort interface {
	// ReserveRun verifies live binding/environment and atomically persists a new
	// immutable reservation before ANY model, memory or tool effect. Existing IDs
	// always return ErrRunUsed, even with identical input, a successor attempt or
	// another SDK instance. All errors (including ambiguous commit outcomes) refuse
	// execution. Retain accepted/uncertain reservations across crashes and failures;
	// never release them automatically. This API supports no reconciled resume.
	// A later Admit must resolve this exact reservation from authoritative state.
	ReserveRun(context.Context, RunReservation) error
	Admit(context.Context, Admission, func(context.Context) error) error
}

// PolicyPort binds authored permission/policy pins to host policy before effects.
// Evaluate uses verified host facts; caller-written principal labels cannot
// authenticate. Ask/approval obligations use GatePort; Allow without approval
// uses host atomic admission with authoritative policy/grant verification.
type PolicyPort interface {
	CheckDefinition(context.Context, agentdef.Definition, runtimecontract.Binding, runtimecontract.EnvironmentRef) error
	Evaluate(context.Context, Operation) (PolicyResult, error)
}

type PolicyResult struct {
	Decision    policy.Decision
	Reference   runtimecontract.Reference
	Provenance  []runtimecontract.Reference
	Obligations []gate.ObligationRef // exact immutable intent for each obligation
}

// ResourceResolver returns exact pinned artifact bytes, bounded by the host.
// SDK checks SHA-256. URI resolution and access are host-owned, scoped by Binding.
// This first slice supports text instructions/SOPs/steering/context sources and
// opaque policy/resource pins. Packaged skills and executable hooks are refused.
type ResourceResolver interface {
	Resolve(context.Context, runtimecontract.Binding, agentdef.Ref) ([]byte, error)
}

type MemoryQuery struct {
	Binding  runtimecontract.Binding
	Query    string
	MaxItems int
	MaxBytes int // complete JSON response bound, including references
}

type MemoryItem struct {
	Binding   runtimecontract.Binding
	Reference runtimecontract.Reference
	Content   string
}

// MemoryReader authenticates scope and returns only relevant authorized context.
// SDK independently rejects wrong bindings/owners and oversized responses.
type MemoryReader interface {
	Read(context.Context, MemoryQuery) ([]MemoryItem, error)
}

type Tool struct {
	Definition llmtypes.ToolDefinition
	// Versions pins tool implementation/schema and affected resources. Host admission
	// must reverify these; an empty list cannot bind an executable implementation.
	Versions []runtimecontract.Reference
	Execute  func(context.Context, map[string]any) (ToolResult, error)
}

type ToolResult struct {
	Content string
	IsError bool // known tool-level failure; transport errors remain uncertain
}

// Operation is the versioned, canonical SDK descriptor. Arguments are exact JSON
// bytes from the normalized llmtypes tool input. Host-specific effects/resources
// must be included in Versions or refused by host admission. It is not a plan.
type Operation struct {
	RunID             string                         `json:"run_id"`
	InputDigest       runtimecontract.Digest         `json:"input_digest"`
	Version           string                         `json:"version"`
	ID                string                         `json:"id"`
	Binding           runtimecontract.Binding        `json:"binding"`
	Environment       runtimecontract.EnvironmentRef `json:"environment"`
	DefinitionDigest  string                         `json:"definition_digest"`
	PermissionProfile string                         `json:"permission_profile"`
	TurnID            runtimecontract.TurnID         `json:"turn_id"`
	ToolCallID        runtimecontract.ToolCallID     `json:"tool_call_id"`
	Tool              string                         `json:"tool"`
	Arguments         json.RawMessage                `json:"arguments"`
	Versions          []runtimecontract.Reference    `json:"versions"`
}
