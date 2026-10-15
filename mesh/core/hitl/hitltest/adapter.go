package hitltest

import "context"

// Adapter is the wire-level view of an implementation under test: JSON
// documents in, JSON documents out. A failed call returns a non-nil error and,
// where the implementation has one, the wire error body ({"code": ...}) as the
// byte slice; RunConformance decodes that body with hitl.DecodeError, so an
// implementation that reports failure only through error text cannot pass the
// scenarios that assert typed errors.
//
// Doc arguments follow the schema bundle: Enqueue takes an
// HITLEnqueueRequestCoreV1 (plus whatever extras the implementation needs),
// Get/Await/Withdraw take their command definitions, and the success results
// are HITLItemHandleCoreV1, HITLRetrievalResultCoreV1 and
// HITLTerminalOutcomeCoreV1 respectively.
type Adapter interface {
	Enqueue(ctx context.Context, doc []byte) ([]byte, error)
	Get(ctx context.Context, doc []byte) ([]byte, error)
	Await(ctx context.Context, doc []byte) ([]byte, error)
	Withdraw(ctx context.Context, doc []byte) ([]byte, error)
	// ParticipantResolve answers an item however the implementation does it
	// (Tangent: present, then resolve). response is a HITLResponseCoreV1
	// document; the adapter supplies the participant. The success result is
	// the resolved HITLTerminalOutcomeCoreV1.
	ParticipantResolve(ctx context.Context, itemID string, response []byte) ([]byte, error)
	// ExpireDue runs the implementation's expiry sweep once. It is called only
	// under CapExpiry.
	ExpireDue(ctx context.Context) error
}

// Capability names an optional behavior an implementation may declare.
type Capability string

const (
	// CapExpiry: the implementation enforces expires_at. A late respond or
	// withdraw is refused atomically (with a conflict carrying the expired
	// outcome) even if no sweeper has run; ExpireDue makes the outcome
	// materialize. Scenarios under this capability enqueue an item whose
	// expires_at is already in the past, so the adapter must accept that.
	CapExpiry Capability = "expiry-enforcement"
	// CapCallerIsolation: a caller outside the item's scope receives
	// not_found or unauthorized for get, await and withdraw. Scopes are
	// advisory partitions, not a security boundary.
	CapCallerIsolation Capability = "caller-isolation"
)
