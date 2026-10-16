package retention

import (
	"context"
	"time"
)

// TrimMode is how a store removes rows that are past their envelope age.
type TrimMode int

const (
	// TrimHeadOnly removes only a contiguous prefix of the stream and advances
	// the stream's pruned-through cursor, so readers behind it get an ordinary
	// retention gap. Trim stops at the first row that is not eligible, which
	// means a row of a class that is kept forever holds back everything after
	// it ([Result.Held] counts what it holds back).
	TrimHeadOnly TrimMode = iota
	// TrimPerRow removes eligible rows wherever they sit. How consumers see
	// the holes is the store's own gap story (for example opaque ids and a
	// gap frame that does not advance the cursor).
	TrimPerRow
)

func (m TrimMode) String() string {
	if m == TrimPerRow {
		return "per-row"
	}
	return "head-only"
}

// Caps describes what a store can do.
type Caps struct{ Trim TrimMode }

// Op bounds one call. A store runs it as one short transaction and releases
// the writer between calls.
type Op struct {
	// Limit is the most rows the call may change. Zero means the store's own default.
	Limit int
	// MaxDuration stops the call early when exceeded. Zero means no time bound.
	MaxDuration time.Duration
	// DryRun counts what the call would change, without a Limit, and writes
	// nothing. MaxDuration still applies to the count: a store that cannot
	// finish it in time returns More=true and a Rows that is only a lower
	// bound (zero is allowed), never an error.
	DryRun bool
}

// Result is what one Store call did.
type Result struct {
	// Rows changed (or, in a dry run, that would change).
	Rows int64
	// BytesFreed is an estimate of payload bytes released (tombstone) or rows
	// removed (trim, prune).
	BytesFreed int64
	// More is true when matching rows remain after Limit or MaxDuration.
	More bool
	// Held (Trim only) counts rows that are past their envelope age but stay
	// because a head-only trim stopped at an earlier row. It is always zero
	// for a [TrimPerRow] store.
	Held int64
	// ByClass (Trim only) splits Rows by class.
	ByClass map[string]int64
}

// RollupResult is what one Rollup call did.
type RollupResult struct {
	Rows    int64 // rows counted into buckets and flagged rolled
	Buckets int64 // bucket rows created or incremented
	More    bool
}

// ClassStat is one class's remaining state in a stream.
type ClassStat struct {
	Class string
	// Rows still in the stream (not trimmed).
	Rows int64
	// Oldest is the recorded time of the oldest remaining row; zero if none.
	Oldest time.Time
}

// Store is what a log or hub implements so the [Janitor] can run on it.
// Every method works on one stream. Calls must be idempotent: repeating a
// call, or running two passes back to back, changes nothing the second time.
// A store may keep any extra columns; the contract below is observable
// behaviour, checked by package storetest.
//
// A spec matches rows by class. A store may keep the policy id a row was
// admitted under, for audit; it plays no part in matching (D7 rev 2.2: the
// current configuration governs every row).
type Store interface {
	Caps() Caps

	// Streams lists the streams that have rows or rollup buckets (a stream
	// whose rows are all trimmed still has buckets to prune).
	Streams(ctx context.Context) ([]string, error)
	// TryLock takes the janitor lock for the whole store. ok is false, with no
	// error, when another janitor holds it. Callers must call unlock.
	TryLock(ctx context.Context) (unlock func(), ok bool, err error)

	// Classify gives a class to rows that have none, using classOf on their attributes.
	Classify(ctx context.Context, stream string, classOf func(Attrs) string, op Op) (Result, error)

	// Rollup counts unrolled rows of the spec's class into all of its buckets,
	// once the largest bucket has closed, and flags them rolled.
	Rollup(ctx context.Context, stream string, s RollupSpec, op Op) (RollupResult, error)

	// Tombstone removes the payload of matching rows (never the envelope),
	// sets the stored digest, and flags the row as tombstoned. Rows already
	// tombstoned are skipped. Secret specs match sensitivity=secret in any class.
	Tombstone(ctx context.Context, stream string, s TombstoneSpec, op Op) (Result, error)

	// Trim removes rows that match any of specs (same policy id and class, at
	// or before the spec's cutoff, rolled up if RequireRolled). A head-only
	// store removes the longest contiguous prefix of eligible rows and
	// advances pruned-through; a per-row store removes every eligible row.
	Trim(ctx context.Context, stream string, specs []TrimSpec, op Op) (Result, error)

	// Prune deletes rollup buckets that ended at or before the spec's cutoff.
	Prune(ctx context.Context, stream string, s PruneSpec, op Op) (Result, error)

	// Stats reports the remaining rows and the oldest row per class.
	Stats(ctx context.Context, stream string) ([]ClassStat, error)
}
