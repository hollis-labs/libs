package mesh

import (
	"context"
	"time"
)

// EnrollmentLifecycle describes registry participation, not execution status.
type EnrollmentLifecycle string

const (
	EnrollmentActive  EnrollmentLifecycle = "active"
	EnrollmentRetired EnrollmentLifecycle = "retired"
)

// Agent is an enrollment record. Its stable URN survives successive instances,
// session attachments, relocation and credential rotation. Definition is pinned;
// rebinding applies to a later session, never beneath an existing session.
// Owner is a principal reference, not a permission or an approval grant.
type Agent struct {
	URN        URN                 `json:"urn"`
	Owner      URN                 `json:"owner"`
	Definition DefinitionRef       `json:"definition"`
	Lifecycle  EnrollmentLifecycle `json:"lifecycle"`
}

// AgentInstance records one execution of an enrolled identity. Status describes
// work; Session.State independently describes process/control-plane connectivity.
// BindingFence is the binding lease's fencing token, not a controller epoch.
type AgentInstance struct {
	ID              string         `json:"id"`
	AgentURN        URN            `json:"agent_urn"`
	Definition      DefinitionRef  `json:"definition"`
	NodeRef         string         `json:"node_ref"`
	RuntimeRef      string         `json:"runtime_ref"`
	SessionURN      URN            `json:"session_urn"`
	BindingFence    uint64         `json:"binding_fence"`
	LaunchRecordRef string         `json:"launch_record_ref"`
	Status          InstanceStatus `json:"status"`
	Detail          InstanceDetail `json:"detail"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Session is a pinned conversation/context record. Successive execution
// instances may attach to it; an inactive session may outlive its process.
// State is connectivity/lifecycle, not the result of a task or instance.
type Session struct {
	URN        URN           `json:"urn"`
	AgentURN   URN           `json:"agent_urn"`
	Definition DefinitionRef `json:"definition"`
	ContextRef string        `json:"context_ref"`
	StoreRef   string        `json:"store_ref"`
	State      SessionState  `json:"state"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

// BindingLease is exclusive per AgentURN. The authoritative host allocates a
// strictly increasing FencingToken on each acquisition and checks that token
// on renew/release and authoritative writes. Expiry is not proof of process
// death. These types do not acquire, enforce or release a lease.
//
// Counter glossary (distinct owners and scopes):
//   - binding lease: registry/mesh, per agent URN; fencing token rejects stale holders;
//   - launch lease: teams launch ledger, per idempotent launch key; fences ledger writes;
//   - controller epoch: process shim, per controller attachment; fences stale control;
//   - pin lock: workspace, per materialization; fixes pinned artifacts for that workspace.
//
// The latter three contracts are owned elsewhere and are not binding tokens.
type BindingLease struct {
	AgentURN     URN       `json:"agent_urn"`
	SessionURN   URN       `json:"session_urn"`
	InstanceID   string    `json:"instance_id"`
	Holder       URN       `json:"holder"`
	ExpiresAt    time.Time `json:"expires_at"`
	FencingToken uint64    `json:"fencing_token"`
}

// ResolveRequest addresses an enrolled identity and optionally its pinned
// session. Caller authentication and authorization are supplied by the host.
// Remote-reference enrollment is not specified by this contract.
type ResolveRequest struct {
	AgentURN   URN `json:"agent_urn"`
	SessionURN URN `json:"session_urn,omitempty"`
}

// ResolveResult is a host-verified snapshot. An idle enrollment need not have
// an instance, session or binding. Pins refer to immutable semantic revisions;
// checking pinned content and current binding authority belongs to the host.
type ResolveResult struct {
	Agent    Agent          `json:"agent"`
	Session  *Session       `json:"session,omitempty"`
	Instance *AgentInstance `json:"instance,omitempty"`
	Binding  *BindingLease  `json:"binding,omitempty"`
}

// Resolver is the resolve contract implemented by the authoritative enrollment
// host. Resolve verifies enrollment and definition pins and reports the binding;
// it does not imply enrollment, lease acquisition or process launch. No resolver
// implementation or lease/enrollment verbs are provided by this package.
type Resolver interface {
	Resolve(context.Context, ResolveRequest) (ResolveResult, error)
}
