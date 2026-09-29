package toolresult

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. The errors returned by [Cache] carry the Nanite wording
// (for example `cached result "01J..." not found or expired`) and wrap one of
// these, so callers test with [errors.Is] and agents see the readable text.
var (
	// ErrNotFound means no entry with that id exists in that scope. It is
	// returned for an absent id and for an id owned by another scope alike,
	// so a caller cannot probe for other scopes' ids.
	ErrNotFound = errors.New("cached result not found or expired")
	// ErrExpired means the entry exists but its TTL has passed.
	ErrExpired = errors.New("cached result has expired")
	// ErrBodyNotStored means only metadata was cached because the body
	// exceeded [Config.HardCapBytes].
	ErrBodyNotStored = errors.New("cached result body not stored (exceeded hard cap)")
	// ErrEmptyScope means a scope was required and the empty string was
	// given.
	ErrEmptyScope = errors.New("scope is required")
)

// Entry is one stored tool result. Scope is the isolation key: it must be
// derived by the host (a session or caller identity it authenticated), never
// taken from tool arguments the model controls.
type Entry struct {
	// ID is the opaque pointer id, unique across scopes.
	ID string
	// Scope is the owner; [Store.Get] only returns entries of the scope asked
	// for.
	Scope string
	// Tool is the producing tool's name, for diagnostics.
	Tool string
	// CallID is the model's tool-call id, for diagnostics.
	CallID string
	// CreatedAt and ExpiresAt bound the entry's life. Stores keep whole
	// seconds.
	CreatedAt, ExpiresAt time.Time
	// ByteSize is the length of the original body, stored or not.
	ByteSize int
	// Body is the original body; empty when BodyStored is false.
	Body string
	// BodyStored is false for a metadata-only entry (body over the hard
	// cap).
	BodyStored bool
}

// Store is the persistence port. Implementations must be safe for concurrent
// use. [github.com/hollis-labs/go-toolresult/storetest.Run] is the conformance
// suite.
type Store interface {
	// Put inserts e. It fails if e.ID already exists.
	Put(ctx context.Context, e Entry) error
	// Get returns the entry with that id owned by scope, or [ErrNotFound]
	// when it is absent or owned by another scope; the two cases must be
	// indistinguishable. Get does not check expiry; [Cache] does.
	Get(ctx context.Context, scope, id string) (Entry, error)
	// DeleteExpired removes entries whose ExpiresAt is before the given time
	// and returns how many it removed.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}
