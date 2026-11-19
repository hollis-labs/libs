// Package gatecrasher coordinates operation-bound human approvals over an
// existing HITL owner. Hosts retain identity, policy facts, durable storage,
// live origin fencing and atomic operation admission. This library executes no
// effects and provides no production persistence or provider adapter.
package gatecrasher

import (
	"context"
	"errors"
	"time"

	"github.com/hollis-labs/libs/mesh/core/gate"
	"github.com/hollis-labs/libs/mesh/core/hitl"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

var (
	ErrConflict    = errors.New("gatecrasher: immutable request conflict")
	ErrNotFound    = errors.New("gatecrasher: route not found")
	ErrCAS         = errors.New("gatecrasher: journal revision conflict")
	ErrBusy        = errors.New("gatecrasher: delivery lease held")
	ErrPending     = errors.New("gatecrasher: interaction pending")
	ErrUnknown     = errors.New("gatecrasher: reconciliation required")
	ErrLocalPath   = errors.New("gatecrasher: policy allows independent local path; no human decision issued")
	ErrPolicy      = errors.New("gatecrasher: policy refused")
	ErrInteraction = errors.New("gatecrasher: invalid authoritative interaction")
)

// InteractionPort is bound by the host to ONE existing authenticated owner.
// Enqueue must be idempotent on source scope+key, conflict on snapshot changes,
// and recover the same item even after an ambiguous response. Respond and
// Withdraw retain the owner's atomic expiry and first-terminal-wins CAS.
// Host adapters may map this profile to an existing native profile only if the
// exact request snapshot/outcome round-trip survives. Never enqueue into two
// owners or bypass that owner's auth rules.
type InteractionPort interface {
	Enqueue(context.Context, hitl.EnqueueRequest) (hitl.Handle, error)
	Get(context.Context, hitl.GetCommand) (hitl.RetrievalResult, error)
	Await(context.Context, hitl.AwaitCommand) (hitl.RetrievalResult, error)
	Withdraw(context.Context, hitl.WithdrawCommand) (hitl.Outcome, error)
	Respond(context.Context, hitl.RespondCommand) (hitl.Outcome, error)
}

// ResponseAttempt carries untrusted assertions/proof input. Authorize must use
// authenticated host context, exact snapshot, owner, revision and chosen option
// to enforce responder/grant authority, no-self-approval and required presence.
// It returns the verified participant to record; constructing it is not proof.
type ResponseAttempt struct {
	OptionID         string
	ExpectedRevision int64
	Participant      hitl.Participant
}
type ResponseAuthority interface {
	Authorize(context.Context, gate.Request, gate.Ticket, ResponseAttempt) (hitl.Participant, error)
}

// Origin.Send must revalidate current host authority/grants and fence the exact
// live request through the physical write, including generation, launch mode,
// recomputed operation digest, native options and scope/request expiry. Earlier
// coordinator verification is insufficient if authority changes before writing.
// Fence or idempotently reject competing sends even after a delivery lease
// expires; a negative query must account for any earlier in-flight send.
// Reconcile is a historical query: it can acknowledge the OLD exact binding
// after expiry/replacement without authorizing any new send. It returns Pending
// ONLY with authoritative evidence no reply was accepted, any in-flight write
// is fenced or idempotent, and retrying this same live request is safe. Sent is
// not an acknowledgement.
// Neither a missing receipt nor a network error proves a safe retry.
type Origin interface {
	Capabilities(context.Context, gate.AdapterScope) (gate.Capabilities, error)
	Validate(context.Context, gate.Request) error
	Send(context.Context, gate.Request, gate.Resolution) (gate.DeliveryReceipt, error)
	Reconcile(context.Context, gate.Request, gate.Resolution) (gate.DeliveryReceipt, error)
}

// Route is a routing journal, never a second writable human decision. Request,
// owner and caller scope are immutable from Reserve, before external enqueue;
// Ticket is immutable after attachment. Decisions reload from HITL. Version is
// a monotonic CAS/fencing token. Every value crossing Journal is detached.
type Route struct {
	OwnerRef    string
	CallerScope string
	Version     uint64
	Request     gate.Request
	Ticket      *gate.Ticket
	Delivery    *gate.DeliveryReceipt
	DecisionRef string
	Attempts    uint64
	LeaseUntil  time.Time
	Executions  []gate.ExecutionReceipt
}

// Journal implementations must atomically Reserve by IdempotencyKey (same
// snapshot AND pinned owner/caller scope return the existing route; mismatch
// ErrConflict), and enforce CAS, immutable Request/OwnerRef/CallerScope/Ticket
// and incrementing Version in Swap. Find matches the ENTIRE ticket including
// owner and native revision. FindDecision indexes delivery correlation only,
// not an independently writable decision. List returns recoverable
// routes (including unattached intent and unknown delivery), in bounded pages
// using an opaque cursor. Durable adapters must retain all of this across
// process replacement. No distributed transaction with HITL is required.
type Journal interface {
	Reserve(context.Context, Route) (Route, error)
	Load(context.Context, string) (Route, error)
	Find(context.Context, gate.Ticket) (Route, error)
	FindDecision(context.Context, string, runtimecontract.Binding) (Route, error)
	Swap(context.Context, string, uint64, Route) (Route, error)
	List(context.Context, string, int) ([]Route, string, error)
}
