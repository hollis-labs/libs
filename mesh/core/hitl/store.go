package hitl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// Store errors. A Store returns these (wrapped is fine) and Service maps them.
var (
	// ErrRevisionMismatch: Swap's expected revision is not the stored revision.
	ErrRevisionMismatch = errors.New("hitl: store: revision mismatch")
	// ErrTerminalRecord: Swap on a record that is already terminal.
	ErrTerminalRecord = errors.New("hitl: store: record is terminal")
	// ErrInvalidRecord: a record or swap that breaks the record invariants
	// (see CheckSwap).
	ErrInvalidRecord = errors.New("hitl: store: invalid record")
)

// Record is one durable interaction as a Store keeps it. Every field except
// State, Revision, UpdatedAt and Outcome is immutable after Create.
type Record struct {
	ItemID string
	// CallerScope is the caller scope the record belongs to (the source
	// application id). Idempotency is unique per (CallerScope, IdempotencyKey).
	CallerScope    string
	IdempotencyKey string
	// Digest is the canonical digest of the immutable request excluding the
	// idempotency key; equal digest means "same request".
	Digest string
	// Kind is the request kind, kept for response matching.
	Kind string
	// Request is the canonical JSON request snapshot.
	Request   []byte
	State     State
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
	// ExpiresAt is the request deadline, if any.
	ExpiresAt *time.Time
	// Outcome is set exactly when State is terminal.
	Outcome Outcome
}

// Validate checks the invariants that hold for every stored record.
func (r Record) Validate() error {
	switch {
	case r.ItemID == "" || r.CallerScope == "" || r.IdempotencyKey == "":
		return fmt.Errorf("%w: item id, caller scope and idempotency key are required", ErrInvalidRecord)
	case !r.State.Valid():
		return fmt.Errorf("%w: unknown state %q", ErrInvalidRecord, r.State)
	case r.Revision < 1:
		return fmt.Errorf("%w: revision must be at least 1", ErrInvalidRecord)
	case r.State.IsTerminal() && r.Outcome == nil:
		return fmt.Errorf("%w: terminal record has no outcome", ErrInvalidRecord)
	case !r.State.IsTerminal() && r.Outcome != nil:
		return fmt.Errorf("%w: nonterminal record has an outcome", ErrInvalidRecord)
	case r.Outcome != nil && (r.Outcome.OutcomeState() != r.State ||
		r.Outcome.OutcomeItemID() != r.ItemID || r.Outcome.OutcomeRevision() != r.Revision):
		return fmt.Errorf("%w: outcome does not match the record", ErrInvalidRecord)
	}
	return nil
}

// CheckSwap enforces the compare-and-set rules every Store must apply, so
// implementations behave identically: the stored record must not be terminal
// (ErrTerminalRecord), its revision must equal expected (ErrRevisionMismatch),
// the next record must be valid, keep every immutable field, advance the
// revision by exactly one, and follow CanTransition (all ErrInvalidRecord).
func CheckSwap(prev Record, expected int64, next Record) error {
	if prev.State.IsTerminal() {
		return ErrTerminalRecord
	}
	if prev.Revision != expected {
		return ErrRevisionMismatch
	}
	if err := next.Validate(); err != nil {
		return err
	}
	switch {
	case next.ItemID != prev.ItemID, next.CallerScope != prev.CallerScope,
		next.IdempotencyKey != prev.IdempotencyKey, next.Digest != prev.Digest,
		next.Kind != prev.Kind, !bytes.Equal(next.Request, prev.Request),
		!next.CreatedAt.Equal(prev.CreatedAt), !sameTimePtr(next.ExpiresAt, prev.ExpiresAt):
		return fmt.Errorf("%w: immutable field changed", ErrInvalidRecord)
	case next.Revision != prev.Revision+1:
		return fmt.Errorf("%w: revision must advance by one (%d -> %d)", ErrInvalidRecord, prev.Revision, next.Revision)
	case !CanTransition(prev.State, next.State):
		return fmt.Errorf("%w: illegal transition %s -> %s", ErrInvalidRecord, prev.State, next.State)
	}
	return nil
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// Store is the persistence boundary of Service. It must make Create and Swap
// atomic: any number of concurrent callers see exactly one winner, and a
// terminal record never changes. hitltest.RunStoreContract checks this.
//
// Everything Service needs for atomic terminal transitions (first terminal
// wins, expiry versus respond) is a Swap; a Store needs no other transaction.
type Store interface {
	// Create stores rec if no record exists for (rec.CallerScope,
	// rec.IdempotencyKey) and returns (rec, true, nil). Otherwise it stores
	// nothing and returns (existing, false, nil), whatever the digest.
	Create(ctx context.Context, rec Record) (Record, bool, error)
	// Get returns the record, or ErrNotFound.
	Get(ctx context.Context, itemID string) (Record, error)
	// Swap replaces the record if it is at revision expected, per CheckSwap.
	Swap(ctx context.Context, itemID string, expected int64, next Record) error
	// DueForExpiry returns up to limit nonterminal records whose ExpiresAt is
	// at or before now, earliest first.
	DueForExpiry(ctx context.Context, now time.Time, limit int) ([]Record, error)
}
