package webauthn

import (
	"context"
	"encoding/json"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

// Ceremony is server-owned state. Never reconstruct it from a client request.
type Ceremony struct {
	Kind         string
	Challenge    []byte
	Origin       string
	ExpiresAt    time.Time
	Registration *wa.SessionData
	AllowedIDs   [][]byte
	RemoveID     []byte
}

// Proof is recorded only after signature, UV, counter and single-use checks.
// It is evidence, not a bearer authorization. The caller still owns operation
// binding, current requester policy and once-only execution of the effect.
type Proof struct {
	CredentialID []byte
	Challenge    []byte
	Origin       string
	Assertion    json.RawMessage
	VerifiedAt   time.Time
	ExpiresAt    time.Time
}

// Snapshot contains one subject's public credentials and short-lived ceremonies.
// Version is monotonically increasing, including after all keys are removed.
// User.ID must remain stable. Pending and Proofs must be durable with counters.
type Snapshot struct {
	Version int64
	User    User
	Pending map[string]Ceremony
	Proofs  map[string]Proof
}

// Store is scoped to a single RP/trust authority. Load returns an isolated copy;
// a missing subject returns an empty snapshot at version zero. CompareAndSwap
// atomically commits credentials, counters, ceremonies and proof receipts only
// if expected still matches. A credential ID cannot belong to two subjects.
// Implementations must return ErrConflict on stale state and propagate write
// failures. Callers must not accept a proof until its commit succeeds.
type Store interface {
	Load(context.Context, string) (Snapshot, error)
	CompareAndSwap(context.Context, string, int64, Snapshot) error
}
