package streamhub

import (
	"context"
	"iter"
)

// Log is the persistence seam: an append-only, per-stream sequence of
// records that assigns its own cursor. Applications implement it over their
// own tables; [NewMemoryLog] is the in-memory implementation and
// hubtest.Conformance is the acceptance suite for any backend.
//
// The contract mirrors the MCP go-sdk EventStore.After rule: a Log returns an
// error immediately when anything after the cursor was dropped, and never a
// partial result. It differs in that Append assigns and returns the cursor.
//
// A Log must be safe for concurrent use. A Log is not required to be
// transactional across streams.
type Log interface {
	// Append atomically assigns the next Seq (starting at 1 per stream) and
	// a timestamp, stores the record and returns it. Concurrent Appends to
	// one stream must receive distinct, gap-free Seqs.
	Append(ctx context.Context, stream string, e Event) (Record, error)

	// After yields the records with Seq > after in ascending order, paging
	// lazily. If any record with Seq > after has been pruned it yields a
	// *GapError (matching ErrGap) before any record; if after is beyond the
	// stream head it yields a *GapError with reason GapCursorAhead (matching
	// ErrCursorAhead). A record is never yielded together with an error for
	// the same call's opening cursor. Iteration may end early when the
	// caller stops ranging. An unknown stream is an empty stream.
	After(ctx context.Context, stream string, after Seq) iter.Seq2[Record, error]

	// Head reports the stream summary. An unknown stream yields the zero Head.
	Head(ctx context.Context, stream string) (Head, error)

	// Trim removes records that fall outside r, oldest first, and advances
	// PrunedThrough accordingly.
	Trim(ctx context.Context, stream string, r Retention) error

	// Close releases the log. Later calls return ErrClosed.
	Close() error
}

// Forgetter is an optional extension of [Log]. A Hub calls Forget once a
// stream has ended and its retention grace period has passed, so an
// in-memory Log can release the stream. Durable logs normally do not
// implement it: their records outlive the hub's memory of the stream.
type Forgetter interface {
	// Forget removes every trace of the stream, including its Seq counter.
	Forget(ctx context.Context, stream string) error
}
