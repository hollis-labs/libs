package chat

import (
	"context"
	"time"
)

// Role is what an attachment may do.
type Role string

// The attachment roles.
const (
	// RoleViewer reads the conversation.
	RoleViewer Role = "viewer"
	// RoleController reads and may hold the input lease.
	RoleController Role = "controller"
)

// Valid reports whether r is one of the roles.
func (r Role) Valid() bool { return r == RoleViewer || r == RoleController }

// StreamMode is what an attachment's event stream carries.
type StreamMode string

// The stream modes.
const (
	// StreamDeltas carries every event.
	StreamDeltas StreamMode = "deltas"
	// StreamCommitted carries every event except part.delta.
	StreamCommitted StreamMode = "committed"
)

// Valid reports whether m is one of the modes.
func (m StreamMode) Valid() bool { return m == StreamDeltas || m == StreamCommitted }

// Conversation is the stored conversation record.
type Conversation struct {
	ID    string
	Agent string
	// Scope is what the conversation is durable for: a seat ID or an
	// application ID.
	Scope string
	Owner string
	Kind  string
	// Generation starts at 1 and rises when the runner's side is replaced.
	// The Control is its only authority.
	Generation uint64
	RunnerRef  string
	// ActiveRun is the run in progress, or empty.
	ActiveRun string
	// Lease is the stored lease, which may have expired: compare ExpiresAt
	// with the clock. Nil when none is stored.
	Lease *Lease
	// Closed is true once the conversation was reset; Successor is the
	// conversation that replaced it.
	Closed    bool
	Successor string
	CreatedAt time.Time
}

// LeaseAt returns the lease if it is unexpired at now.
func (c Conversation) LeaseAt(now time.Time) (Lease, bool) {
	if c.Lease == nil || !now.Before(c.Lease.ExpiresAt) {
		return Lease{}, false
	}
	return *c.Lease, true
}

// NewConversation is what a Control needs to create a conversation.
type NewConversation struct {
	ID    string
	Agent string
	Scope string
	Owner string
	Kind  string
	At    time.Time
}

// Attachment is one client's view of a conversation. Its ID is opaque and is
// not a credential.
type Attachment struct {
	ID             string
	ConversationID string
	Role           Role
	Stream         StreamMode
	// Generation is the conversation's generation when the attachment was
	// made. An attachment from an older generation is stale.
	Generation uint64
	CreatedAt  time.Time
}

// Lease is the input lease: the right to submit, cancel and answer approvals.
type Lease struct {
	// Holder is the attachment that holds it.
	Holder    string
	ExpiresAt time.Time
}

// LeaseChange is the result of an operation that may move the lease.
type LeaseChange struct {
	Lease Lease
	// Acquired is true when the holder changed (a first acquire, an acquire
	// after expiry, a takeover) and false for a renewal or no change.
	Acquired bool
	// Previous is the holder this change displaced, when there was an
	// unexpired one.
	Previous string
}

// Control stores conversations, attachments and the input lease, and is the
// only authority for a conversation's generation. The lease rules are one
// contract: chattest.ControlConformance checks any implementation against
// them. memstore and sqlitestore are the built-in implementations.
//
// Every method is one atomic step. Methods that depend on time take now from
// the caller, so an implementation needs no clock of its own. Errors:
// [ErrNotFound] for an unknown conversation, a [*ClosedError] for a
// conversation that was reset (every method but Get), [ErrUnknownAttachment],
// a [*StaleError] for an attachment from an older generation,
// [ErrNotController], a [*LeaseHeldError], [ErrLeaseRequired] and [ErrBusy].
type Control interface {
	// Ensure returns the agent's current conversation, storing nc as that
	// conversation (generation 1) when the agent has none. created reports
	// which happened.
	Ensure(ctx context.Context, nc NewConversation) (conv Conversation, created bool, err error)
	// Get returns a conversation record, closed or not.
	Get(ctx context.Context, conversationID string) (Conversation, error)
	// Reset closes the conversation and stores next as the agent's current
	// one. It fails with ErrBusy while a run is active and with
	// ErrLeaseRequired when an unexpired lease is held by an attachment other
	// than attachmentID. attachmentID may be empty when nobody holds the lease.
	Reset(ctx context.Context, conversationID, attachmentID string, next NewConversation, now time.Time) (Conversation, error)
	// SetRunnerRef stores the runner's reference.
	SetRunnerRef(ctx context.Context, conversationID, ref string) error

	// Attach stores a with the conversation's current generation and returns
	// it. a.ID, a.Role, a.Stream and a.CreatedAt are the caller's.
	Attach(ctx context.Context, a Attachment) (Attachment, error)
	// Attachment returns a current attachment.
	Attachment(ctx context.Context, conversationID, attachmentID string) (Attachment, error)
	// Detach removes a current attachment. released reports that it held the
	// unexpired lease, which is released with it.
	Detach(ctx context.Context, conversationID, attachmentID string, now time.Time) (released bool, err error)

	// Acquire gives the lease to a controller attachment, or renews it when
	// that attachment already holds it. It fails with a *LeaseHeldError while
	// another attachment holds an unexpired lease.
	Acquire(ctx context.Context, conversationID, attachmentID string, now time.Time, ttl time.Duration) (LeaseChange, error)
	// Takeover gives the lease to a controller attachment whoever holds it.
	Takeover(ctx context.Context, conversationID, attachmentID string, now time.Time, ttl time.Duration) (LeaseChange, error)
	// Holds returns nil when the attachment holds the unexpired lease, and
	// ErrLeaseRequired otherwise.
	Holds(ctx context.Context, conversationID, attachmentID string, now time.Time) error

	// BeginRun records runID as the active run. With an attachment it first
	// requires the lease: held by that attachment, or free and the attachment
	// a controller, in which case the lease is acquired in the same step.
	// With an empty attachmentID (a run the host started) no lease is needed.
	// It fails with ErrBusy while another run is active, and then changes
	// nothing.
	BeginRun(ctx context.Context, conversationID, runID, attachmentID string, now time.Time, ttl time.Duration) (LeaseChange, error)
	// EndRun clears the active run if it is runID. It is idempotent.
	EndRun(ctx context.Context, conversationID, runID string) error
}

// Bumper is an optional extension of [Control] for a store that does not bump
// the generation on its own. The Service calls it when a runner signals on
// [Runner.Replaced], before it re-reads the generation. Bump raises the
// generation by one, which makes every attachment stale, and clears the lease
// and the active run. A host-supplied Control that bumps in its own
// transaction before the signal does not implement it.
type Bumper interface {
	Bump(ctx context.Context, conversationID string) (Conversation, error)
}
