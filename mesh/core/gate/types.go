// Package gate defines experimental operation-bound approval ports. HITL owns
// the immutable decision; hosts own authority, live admission and grant claims.
// No value here is a credential, an effect executor or a second decision store.
package gate

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

const ContractVersion = "1"

var (
	ErrInvalid     = errors.New("gate: invalid contract")
	ErrUnsupported = errors.New("gate: unsupported capability")
	ErrBinding     = errors.New("gate: binding mismatch")
	ErrExpired     = errors.New("gate: expired")
	ErrDenied      = errors.New("gate: not allowed")
	ErrAuthority   = errors.New("gate: authority verification failed")
)

// NativeRequestID retains the exact JSON string or number, including integers
// beyond float64 precision. Validate before retaining; never coerce through any.
type NativeRequestID json.RawMessage

type DecisionKind string

const (
	Allow  DecisionKind = "allow"
	Deny   DecisionKind = "deny"
	Cancel DecisionKind = "cancel"
)

type ScopeKind string

const (
	Once            ScopeKind = "once"
	Bounded         ScopeKind = "bounded"
	Session         ScopeKind = "session"
	PolicyAmendment ScopeKind = "policy_amendment"
)

// Scope is an exact advertised choice, not an ordered privilege lattice.
// Once requires MaxUses=1; bounded requires a positive limit. Session and policy
// amendment remain distinct and require explicit adapter support. Zero MaxUses
// on those scopes means no advertised use limit, never an implicit once grant.
type Scope struct {
	Kind      ScopeKind `json:"kind"`
	Target    string    `json:"target"`
	MaxUses   uint64    `json:"max_uses"`
	ExpiresAt time.Time `json:"expires_at"`
}
type NativeOption struct {
	ID    string       `json:"id"`
	Kind  DecisionKind `json:"kind"`
	Scope Scope        `json:"scope"`
	// Payload stays in scoped adapter custody. Do not put it in routine audit logs.
	Payload json.RawMessage `json:"payload,omitempty"`
}

type AdapterScope struct {
	AdapterID  string `json:"adapter_id"`
	Protocol   string `json:"protocol"`
	LaunchMode string `json:"launch_mode"`
}

// Capabilities apply only to this exact adapter/protocol/launch mode. Advertising
// support is not proof a provider actually emitted an enforceable callback.
type Capabilities struct {
	Adapter          AdapterScope   `json:"adapter"`
	OperationClasses []string       `json:"operation_classes"`
	DecisionKinds    []DecisionKind `json:"decision_kinds"`
	ScopeKinds       []ScopeKind    `json:"scope_kinds"`
	ObligationKinds  []string       `json:"obligation_kinds"`
}

// ObligationRef identifies host-owned mandatory policy intent. Hosts resolve the
// entire immutable intent (including rate/preview parameters); this is not a PDP
// model or a statement that an obligation has been fulfilled.
type ObligationRef struct {
	Kind string                    `json:"kind"`
	Ref  runtimecontract.Reference `json:"ref"`
}

type Request struct {
	ContractVersion  string                          `json:"contract_version"`
	IdempotencyKey   string                          `json:"idempotency_key"`
	Binding          runtimecontract.Binding         `json:"binding"`
	Adapter          AdapterScope                    `json:"adapter"`
	Environment      *runtimecontract.EnvironmentRef `json:"environment,omitempty"`
	EnforcementPoint string                          `json:"enforcement_point"`
	OperationID      string                          `json:"operation_id"`
	OperationClass   string                          `json:"operation_class"`
	// Host hashes the versioned canonical descriptor, including action, targets,
	// authoritative argument/plan bytes, resource versions, environment and effects.
	// This is NOT the HITL idempotency digest or a human-readable summary.
	OperationDigest  runtimecontract.Digest      `json:"operation_digest"`
	ResourceVersions []runtimecontract.Reference `json:"resource_versions,omitempty"`
	TurnID           runtimecontract.TurnID      `json:"turn_id,omitempty"`
	ToolCallID       runtimecontract.ToolCallID  `json:"tool_call_id,omitempty"`
	NativeRequestID  NativeRequestID             `json:"native_request_id"`
	Options          []NativeOption              `json:"options"`
	ExpiresAt        time.Time                   `json:"expires_at"`
	Policy           runtimecontract.Reference   `json:"policy"`
	Provenance       []runtimecontract.Reference `json:"provenance,omitempty"`
	Obligations      []ObligationRef             `json:"obligations,omitempty"`
}

// Ticket identifies the single HITL owner and immutable gate request snapshot.
// AuthorityRef selects that host-owned HITL service; ItemID alone is not global.
type Ticket struct {
	AuthorityRef  string                  `json:"authority_ref"`
	Handle        hitl.Handle             `json:"handle"`
	RequestDigest runtimecontract.Digest  `json:"request_digest"`
	Binding       runtimecontract.Binding `json:"binding"`
}

// Decision is a projection, not independently writable approval state. Verify
// resolves the authoritative HITL item/revision/resolution and authorization.
type Decision struct {
	Ticket          Ticket                 `json:"ticket"`
	Resolution      hitl.ResolutionRecord  `json:"resolution"`
	OptionID        string                 `json:"option_id"`
	Scope           Scope                  `json:"scope"`
	OperationDigest runtimecontract.Digest `json:"operation_digest"`
	AuthorityRef    string                 `json:"authority_ref"`
	ExpiresAt       time.Time              `json:"expires_at"`
}

type ResolutionState string

const (
	Pending   ResolutionState = "pending"
	Decided   ResolutionState = "decided"
	Expired   ResolutionState = "expired"
	Cancelled ResolutionState = "cancelled"
)

type Resolution struct {
	State    ResolutionState `json:"state"`
	Ticket   Ticket          `json:"ticket"`
	Decision *Decision       `json:"decision,omitempty"`
	// Outcome is the existing HITL terminal outcome, not a second writable record.
	Outcome hitl.Outcome `json:"-"`
}
type WaitOptions struct{ MaxWait time.Duration }

// GatePort implementations defensively snapshot accepted requests. Identical
// idempotency key + snapshot returns the same interaction; changed snapshot
// conflicts. Await's context/deadline ends only the wait. Cancel is explicit.
// Response authorization precedes HITL's atomic first-terminal-wins transition.
type GatePort interface {
	Capabilities(context.Context, AdapterScope) (Capabilities, error)
	Request(context.Context, Request) (Ticket, error)
	Await(context.Context, Ticket, WaitOptions) (Resolution, error)
	Cancel(context.Context, Ticket, hitl.CancelCause) (Resolution, error)
}

// AuthorityVerifier is injected from a trusted host. Verify must resolve the
// authoritative HITL outcome (including option selection and policy provenance),
// authenticate/authorize its responder, enforce no-self-approval/presence where
// required, and verify current host/enforcement point, binding, environment,
// resources, grants and all obligations. A supplied Participant/proof/AuthorityRef
// or shape-valid receipt is never sufficient. Errors always refuse admission.
// Atomic once/limited consumption is a separate executor-owned step; execute
// only after that live claim, and retain an uncertain claim until reconciled.
type AuthorityVerifier interface {
	Verify(context.Context, Request, Decision) error
}

// Admission carries live host facts, not facts copied from a decision. The
// executor recomputes OperationDigest from authoritative bytes immediately before
// admission, then verifies authority and atomically claims the grant. Hosts must
// fence binding/lease changes across that claim and effect start (avoid TOCTOU).
type Admission struct {
	Binding          runtimecontract.Binding
	Adapter          AdapterScope
	EnforcementPoint string
	OperationDigest  runtimecontract.Digest
	Now              time.Time
}

type DeliveryStatus string

const (
	DeliveryPending      DeliveryStatus = "pending"
	DeliverySent         DeliveryStatus = "sent"
	DeliveryAcknowledged DeliveryStatus = "acknowledged"
	DeliveryRejected     DeliveryStatus = "rejected"
	DeliveryUnknown      DeliveryStatus = "unknown"
)

type DeliveryReceipt struct {
	DecisionRef     string                  `json:"decision_ref"`
	Binding         runtimecontract.Binding `json:"binding"`
	AdapterID       string                  `json:"adapter_id"`
	NativeRequestID NativeRequestID         `json:"native_request_id"`
	Status          DeliveryStatus          `json:"status"`
	EvidenceRef     string                  `json:"evidence_ref,omitempty"`
}
type ExecutionState string

const (
	ExecutionNotStarted ExecutionState = "not_started"
	ExecutionStarted    ExecutionState = "started"
	ExecutionSucceeded  ExecutionState = "succeeded"
	ExecutionFailed     ExecutionState = "failed"
	ExecutionCancelled  ExecutionState = "cancelled"
	ExecutionUnknown    ExecutionState = "unknown"
)

type ExecutionReceipt struct {
	DecisionRef string                  `json:"decision_ref"`
	OperationID string                  `json:"operation_id"`
	Binding     runtimecontract.Binding `json:"binding"`
	State       ExecutionState          `json:"state"`
	EffectRef   string                  `json:"effect_ref,omitempty"`
	EvidenceRef string                  `json:"evidence_ref,omitempty"`
}

// ReceiptSink reports observations only. Sent is not acknowledged; acknowledged
// is not execution success. Unknown delivery/effects require host reconciliation,
// never automatic replay. Delivery retries stay on the exact live native request.
type ReceiptSink interface {
	RecordDelivery(context.Context, DeliveryReceipt) error
	RecordExecution(context.Context, ExecutionReceipt) error
}
