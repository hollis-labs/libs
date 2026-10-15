package teams

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hollis-labs/substrate/mesh"
)

// Stores return detached values; updates must be atomic, durable and honor
// context cancellation. Versions address immutable definition revisions.
type DefinitionStore interface {
	GetDefinition(context.Context, string, uint64) (Team, error)
	PutDefinition(context.Context, Team) error
}

// Mutate serializes concurrent changes for one run, and commits only if fn
// succeeds. The store increments Version once per committed mutation.
type RosterStore interface {
	Snapshot(context.Context, string) (Roster, error)
	// Immutable retained snapshots authorize in-flight delivery retries.
	// Retain every version referenced by a pending delivery plan until the
	// host has finished or explicitly abandoned that send. An evicted or
	// unknown version returns ErrNotFound; never substitute current roster.
	// Host retention of unreferenced versions is a storage policy decision.
	SnapshotAt(context.Context, string, uint64) (Roster, error)
	Mutate(context.Context, string, func(*Roster) error) error
}

// WithLease serializes operations by key across all host instances, not just
// one launcher. Lost leases must fence writes. Get/Put are called in a lease.
// Pending returns a bounded rotating page (after wraps) to prevent starvation.
// It excludes terminal RoutingReady/Failed records and includes Aborting cleanup.
// Put permits monotonic launch steps or transition to Aborting then Failed;
// terminal records cannot be resurrected. Attempts and deadlines are durable.
type LaunchLedger interface {
	WithLease(context.Context, string, func(context.Context) error) error
	GetLaunch(context.Context, string) (LaunchRecord, error)
	PutLaunch(context.Context, LaunchRecord) error
	Pending(context.Context, string, int) ([]string, error)
}
type SignalStore interface {
	RecordSignal(context.Context, PhaseSignalRecord) error
	ListSignals(context.Context, string, string) ([]PhaseSignalRecord, error)
	GetSignal(context.Context, string, string) (SignalResolution, error)
	ResolveSignal(context.Context, SignalResolution) (SignalResolution, error) // first wins atomically
}
type MemberProvisioner interface {
	// IdempotencyKey identifies a persisted intent. Retries MUST recover the
	// same identity/session, including failures after an external side effect.
	// ErrProvisionFailed explicitly marks an irrecoverable failure; other
	// errors are retryable and retain quota.
	// Retire/Release MUST atomically tombstone Member.Intent.IdempotencyKey.
	// Cleanup touches only resources acquired by Member.Intent.IdempotencyKey,
	// never an unrelated actor lease. A never-acknowledged stub has no Actor.
	// Fence racing/future Provision calls, and clean any resource provisioned
	// before the fence. Release retains the durable/pool identity.
	// The host verifies Definition pins against its registry before provisioning.
	// Every result is an enrolled actor. Fresh enrolls an ephemeral identity
	// keyed to the intent, with no persistent home/mailbox continuity.
	// Pool/durable must return the requested enrolled identity, acquiring one
	// binding lease per actor so two live sessions never share an identity.
	Provision(context.Context, ProvisionRequest) (Member, error)
	// Release ends the stable member session and binding lease, retaining its
	// durable/pool enrollment for a future session.
	Release(context.Context, string, Member) error
	// Retire ends the ephemeral member session, binding lease and enrollment;
	// it shares the same atomic intent-tombstone contract as Release.
	Retire(context.Context, string, Member) error
}
type ProvisionRequest struct {
	IdempotencyKey, RunID, MemberID string
	Slot                            Slot
	Identity                        mesh.URN
	ReservedIdentities              []mesh.URN
	Parent                          string
	Limits                          mesh.Limits
}
type WorkflowLauncher interface {
	LaunchWorkflow(context.Context, string, WorkflowDefinition) (string, error) // idempotent key
	// FailWorkflow fences this launch key, marks an existing run failed and
	// prevents a lost-acknowledgement launch from reappearing during cleanup.
	FailWorkflow(context.Context, string, string) error
}
type TriggerEvaluator interface {
	Evaluate(context.Context, Trigger, Member) (bool, error)
}
type MessageSender interface {
	// BindMessage atomically binds a send key to its immutable content and
	// recipient-plan digest. Changed requests return ErrConflict, even when
	// transport de-duplication remembers only keys.
	BindMessage(context.Context, string, string) error
	// SendMessage honors the retained recipient session and delivery policy.
	// An unavailable session or unsupported queue policy must fail loudly;
	// never retarget a reply to a new session of the same actor. Success means
	// acceptance, including durable queuing for DeliveryAtIdle. New delegation
	// replies must atomically recheck the delegation is non-terminal and both
	// retained member/session pairs are active before queue acceptance.
	SendMessage(context.Context, Delivery) error // idempotent delivery key
}
type RoutingInstaller interface {
	InstallRouting(context.Context, string, TeamRun, Routing) error // idempotent key
	RemoveRouting(context.Context, string) error                    // idempotent run ID
}
type TrustDecision string

const (
	TrustAllow    TrustDecision = "allow"
	TrustDeny     TrustDecision = "deny"
	TrustApproval TrustDecision = "approval"
)

type TrustResolver interface {
	ResolveTrust(context.Context, Member, Slot) (TrustDecision, error)
}
type ApprovalEmitter interface {
	EmitApproval(context.Context, string, Member, Slot) error
}
type Clock interface{ Now() time.Time }
type IDs interface{ NewID() string }

// clone detaches maps/slices at all persistence and snapshot boundaries.
// All public model values are JSON data and validated before persistence.
func clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
