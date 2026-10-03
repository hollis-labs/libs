package streamhub

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Seq is a per-stream cursor. The first record of a stream has Seq 1; the
// zero value means "from the start". Cursors are assigned by a Log and are
// never comparable across streams.
type Seq uint64

// FromLatest is a [SubscribeOptions.After] value that asks for live records
// only: the subscriber starts at the stream head and sees nothing older.
const FromLatest Seq = math.MaxUint64

// Event is what a publisher hands to a Hub: an opaque payload plus a name
// (for example a channel or event type) that filters can look at. The hub
// never interprets either field.
type Event struct {
	Name string
	Data []byte
}

// Record is an Event after a Log has assigned it a cursor and a timestamp.
// A Record obtained from a Log or a Subscription shares its Data with the
// log's storage; treat it as read-only.
type Record struct {
	Seq  Seq
	Name string
	Data []byte
	At   time.Time
}

// Head is a point-in-time summary of one stream in a Log.
type Head struct {
	// Latest is the highest Seq ever assigned; 0 for an empty or unknown stream.
	Latest Seq
	// PrunedThrough is the highest Seq removed by retention; records with
	// Seq <= PrunedThrough are gone. 0 when nothing has been pruned.
	PrunedThrough Seq
	// Dropped counts records removed by retention over the stream's life.
	Dropped uint64
	// Terminated reports that the stream has ended. A Log leaves it false;
	// [Hub.Head] fills it from the hub's own view of the stream.
	Terminated bool
}

// Retention bounds what a Log keeps for one stream. A zero field is
// unlimited. Records are removed oldest first.
type Retention struct {
	// MaxRecords keeps at most this many records.
	MaxRecords int
	// MaxBytes keeps at most this many bytes of Name plus Data.
	MaxBytes int
	// MaxAge removes records older than this, measured by Record.At.
	MaxAge time.Duration
}

func (r Retention) isZero() bool { return r == Retention{} }

// GapReason says why a subscriber cannot be given a contiguous sequence.
type GapReason int

// Gap reasons.
const (
	// GapRetention: records after the requested cursor were removed by retention.
	GapRetention GapReason = iota + 1
	// GapCursorAhead: the requested cursor is beyond the stream head.
	GapCursorAhead
	// GapDropped: this subscriber lost records to its slow-consumer policy.
	GapDropped
)

func (r GapReason) String() string {
	switch r {
	case GapRetention:
		return "retention"
	case GapCursorAhead:
		return "cursor_ahead"
	case GapDropped:
		return "dropped"
	default:
		return fmt.Sprintf("GapReason(%d)", int(r))
	}
}

// Gap is an in-process description of a discontinuity. It is a value, not a
// wire frame: an encoder decides how (and whether) to render it.
type Gap struct {
	Reason GapReason
	// Requested is the cursor the subscriber held when the gap began: the
	// cursor it asked for (GapRetention, GapCursorAhead) or the last record
	// it received before the loss (GapDropped).
	Requested Seq
	// OldestAvailable is the Seq of the first record that follows the gap.
	OldestAvailable Seq
	// Latest is the stream head when the gap was detected.
	Latest Seq
	// Missed is the number of records lost: exact for GapDropped and
	// GapRetention, the distance past the head for GapCursorAhead.
	Missed uint64
}

// GapError carries a [Gap] as an error. It matches [ErrGap], and also
// [ErrCursorAhead] when the reason is [GapCursorAhead].
type GapError struct{ Gap Gap }

func (e *GapError) Error() string {
	return fmt.Sprintf("streamhub: gap (%s): requested %d, oldest available %d, latest %d, missed %d",
		e.Gap.Reason, e.Gap.Requested, e.Gap.OldestAvailable, e.Gap.Latest, e.Gap.Missed)
}

// Is implements the errors.Is contract for [ErrGap] and [ErrCursorAhead].
func (e *GapError) Is(target error) bool {
	switch target {
	case ErrGap:
		return true
	case ErrCursorAhead:
		return e.Gap.Reason == GapCursorAhead
	default:
		return false
	}
}

// SlowConsumerError is what a subscription returns when a slow-consumer
// policy closed it. LastDelivered is the cursor to resume from: every record
// the subscriber was handed is at or below it, and nothing above it was
// queued for this subscriber.
type SlowConsumerError struct{ LastDelivered Seq }

func (e *SlowConsumerError) Error() string {
	return fmt.Sprintf("streamhub: slow consumer closed; resume after seq %d", e.LastDelivered)
}

// Is matches [ErrSlowConsumer].
func (e *SlowConsumerError) Is(target error) bool { return target == ErrSlowConsumer }

// Sentinel errors. Match them with errors.Is.
var (
	// ErrGap: records after a cursor were dropped. A Log returns it (as a
	// *GapError) instead of a partial result.
	ErrGap = errors.New("streamhub: gap")
	// ErrCursorAhead: a cursor is beyond the stream head.
	ErrCursorAhead = errors.New("streamhub: cursor ahead of stream head")
	// ErrSlowConsumer: a subscription was closed by its slow-consumer policy.
	ErrSlowConsumer = errors.New("streamhub: slow consumer")
	// ErrTerminated: the stream has ended and accepts no more records.
	ErrTerminated = errors.New("streamhub: stream terminated")
	// ErrUnknownStream: the stream is not open (or its name is empty).
	ErrUnknownStream = errors.New("streamhub: unknown stream")
	// ErrClosed: the hub, log or subscription is closed.
	ErrClosed = errors.New("streamhub: closed")
)
