package session

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

var (
	ErrInvalid     = errors.New("session: invalid request")
	ErrUnsupported = errors.New("session: unsupported mode")
	ErrConflict    = errors.New("session: concurrent transition or duplicate binding")
	ErrNotFound    = errors.New("session: conversation not found")
	ErrStale       = errors.New("session: stale or fenced binding")
	ErrLease       = errors.New("session: input lease refused")
)

type Mode string

const (
	Fresh    Mode = "fresh"
	Continue Mode = "continue"
	Relaunch Mode = "relaunch"
	Fork     Mode = "fork"
)

type Phase string

const (
	Fenced Phase = "fenced"
	Active Phase = "active"
)

type Reason string

const (
	UserContinue Reason = "continue"
	Crash        Reason = "crash"
	Quota        Reason = "quota"
	Recovery     Reason = "recovery"
)

// Scope is an explicitly selected conversation under an authenticated host
// owner/operative. Struct fields are assertions; the host authenticates callers.
type Scope struct {
	OwnerID        runtimecontract.OwnerID
	OperativeID    runtimecontract.OperativeID
	ConversationID runtimecontract.ConversationID
}

func scope(b runtimecontract.Binding) Scope {
	return Scope{b.OwnerID, b.OperativeID, b.ConversationID}
}

// Record is authoritative host state. Version is an opaque CAS token, not an
// event cursor. A fenced record reserves an attempt even when preparation fails;
// a new recovery attempt must name it as parent and advance its generation.
type Record struct {
	Binding runtimecontract.Binding
	Parent  *runtimecontract.Binding
	Phase   Phase
	Reason  Reason
	Version string
}

type MessageKind string

const (
	Event    MessageKind = "event"
	Approval MessageKind = "approval"
)

// Message records a bound event or approval delivery request. Reference points
// to host-verified immutable content. Admission does not authorize a tool effect;
// gate/environment admission still verifies decision, digest, expiry and grants.
type Message struct {
	Binding   runtimecontract.Binding
	ID        string
	Kind      MessageKind
	Reference runtimecontract.Reference
}

// Conversations stores owner-scoped records. Create reserves a conversation
// once; duplicate creation returns ErrConflict. CompareAndSwap must atomically
// compare the opaque version and enforce unique attempt IDs, immutable scope and
// seat, and monotonic generations. A same-binding fenced->active transition is
// allowed; attempts are never reused after a failed transition.
// A mutation error must definitively mean no commit. Remote adapters must resolve
// ambiguous transport outcomes against durable state before returning; callers
// must not replay a write merely because a response was lost.
//
// Admit must atomically check exact current Active binding at durable delivery,
// deduplicate Message.ID within that binding, and commit the message/cursor.
// Same ID with different immutable content returns ErrConflict.
// Delayed consumers must recheck binding again before acting. Approval delivery
// must also use the verified gate contract; storing a message grants no authority.
// Every method authenticates caller access; a Scope is not proof of identity.
type Conversations interface {
	Create(context.Context, Record) (Record, error)
	Load(context.Context, Scope) (Record, error)
	CompareAndSwap(context.Context, Scope, string, Record) (Record, error)
	Admit(context.Context, Message) (runtimecontract.CommittedCursor, error)
}

type WriterID string
type LeaseID string

// Lease is host-issued and bound to one exact attempt and writer. An opaque ID
// must change on reacquisition so a late prior writer cannot regain admission.
type Lease struct {
	ID        LeaseID
	Binding   runtimecontract.Binding
	Writer    WriterID
	ExpiresAt time.Time
}

type Input struct {
	ID    string
	Lease Lease
	Text  string
}

// InputLeases shares the authoritative admission boundary with Conversations.
// Acquire/Renew atomically verify exact Active binding and one unexpired writer.
// Submit atomically checks active binding, current lease ID/writer and expiry at
// actual delivery, and deduplicates input ID. Queue draining must recheck too.
// Same ID with different immutable content returns ErrConflict.
// Revoke is idempotent and revokes only the specified binding, never its successor.
// Ports must use a host clock, not a caller's ExpiresAt, to decide lease validity.
type InputLeases interface {
	Acquire(context.Context, runtimecontract.Binding, WriterID) (Lease, error)
	Renew(context.Context, Lease) (Lease, error)
	Revoke(context.Context, runtimecontract.Binding) error
	Submit(context.Context, Input) (runtimecontract.CommittedCursor, error)
}

type PrepareRequest struct {
	Binding     runtimecontract.Binding
	Environment runtimecontract.EnvironmentRef
	Recovery    *Pack
}

// Prepared is an inert candidate, with an instance-scoped Close. Ready verifies
// resources and host capabilities and returns the exact prepared binding. Model,
// tool and input effects remain prohibited until host Active admission. Close
// must never close a successor or another candidate for the same logical binding.
type Prepared interface {
	Ready(context.Context) (runtimecontract.Binding, error)
	Close(context.Context) error
}

type Runtimes interface {
	Prepare(context.Context, PrepareRequest) (Prepared, error)
}

// Environments verifies current issued receipt, workspace, grants and lease for
// the exact target. It provisions nothing. Runtime readiness rechecks these after
// potentially slow reconciliation, and each later effect rechecks admission.
type Environments interface {
	Verify(context.Context, runtimecontract.Binding, runtimecontract.EnvironmentRef) error
}

type EffectState string

const (
	NotStarted EffectState = "not_started"
	Started    EffectState = "started"
	Succeeded  EffectState = "succeeded"
	Failed     EffectState = "failed"
	Cancelled  EffectState = "cancelled"
	Unknown    EffectState = "unknown"
)

// Operation describes an unfinished or reconciled effect; unknown is evidence
// of uncertainty, never permission to replay. No effect executor is exposed here.
type Operation struct {
	ID       string
	Binding  runtimecontract.Binding
	Digest   runtimecontract.Digest
	State    EffectState
	Evidence *runtimecontract.Reference
}

type ApprovalState string

const (
	Invalidated      ApprovalState = "invalidated"
	ApprovalPending  ApprovalState = "pending"
	ApprovalTerminal ApprovalState = "terminal"
	ApprovalUnknown  ApprovalState = "unknown"
)

// PendingApproval retains an old approval's reconciliation receipt, never a
// transportable native decision. Unknown/terminal receipts cannot be delivered
// to the new runtime; a new generation must request its own gate decision.
type PendingApproval struct {
	Reference runtimecontract.Reference
	Binding   runtimecontract.Binding
	State     ApprovalState
}

// Pack is bounded portable history and reconciliation evidence. Locator carries
// workspace/snapshot/plan/transcript/lease references and a committed cursor.
// It contains no provider-native hidden context or executable replay commands.
type Pack struct {
	Locator    runtimecontract.RecoveryLocator
	Operations []Operation
	Approvals  []PendingApproval
}

// Recoveries owns durable bounded manifests. Checkpoint runs after fencing and
// must drain/checkpoint a reachable old runtime, or reconstruct from committed
// records when it is lost. Load explicitly selects a binding, never newest.
// Save persists reconciled evidence for the successor before preparation. A host
// must preserve prior evidence if a subsequent checkpoint or transition fails.
type Recoveries interface {
	Checkpoint(context.Context, runtimecontract.Binding, Reason) (Pack, error)
	Load(context.Context, runtimecontract.Binding) (Pack, error)
	Save(context.Context, Pack) error
}

// Reconciler queries authoritative effect outcomes and invalidates old approvals.
// It must not execute/repeat effects. It returns the same source locator with
// updated outcomes; uncertainty survives. Implementations must be idempotent.
type Reconciler interface {
	Reconcile(context.Context, runtimecontract.Binding, Pack) (Pack, error)
}

type SearchQuery struct {
	Scope Scope
	Text  string
	Limit int
}

type TranscriptMatch struct {
	Reference runtimecontract.Reference
	Cursor    runtimecontract.CommittedCursor
	Excerpt   string
}

// Transcripts locates/searches authoritative scoped content. Hosts authenticate
// access and bound query work/results. JSONL and other storage adapters live in
// consumers; a locator alone makes no claim that a provider resumed its context.
type Transcripts interface {
	Locate(context.Context, Scope) (runtimecontract.Reference, error)
	Search(context.Context, SearchQuery) ([]TranscriptMatch, error)
}

type Ports struct {
	Conversations Conversations
	Input         InputLeases
	Runtimes      Runtimes
	Environments  Environments
	Recoveries    Recoveries
	Reconciler    Reconciler
	Transcripts   Transcripts
}
