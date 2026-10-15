package streamhub

import "fmt"

type policyKind int

const (
	policyCloseAndResume policyKind = iota // zero value: the default
	policyBlock
	policyDropNewest
	policyDropOldest
	policyEvictAfterN
)

// SlowPolicy says what a Hub does when a subscriber's buffer is full and a new
// record arrives. The zero value is [CloseAndResume]. Choose per
// subscription through [SubscribeOptions.Policy].
//
// The policy applies to live records. Replay from the log is driven by the
// consumer (it waits for buffer space) and is never lossy.
type SlowPolicy struct {
	kind policyKind
	n    int
}

// The slow-consumer policies. Each states what a subscriber may lose.
var (
	// CloseAndResume closes the subscription with a [SlowConsumerError]
	// whose LastDelivered is a resume cursor. Nothing is lost while the log
	// still retains the records after that cursor: the subscriber drains its
	// buffer, then resumes with Subscribe(After: LastDelivered). This is the
	// default.
	CloseAndResume = SlowPolicy{kind: policyCloseAndResume}

	// Block makes Publish wait until the subscriber has room, bounded by the
	// publisher's context. Nothing is lost while the publisher waits; if the
	// context ends first, the record is counted as dropped for that
	// subscriber and reported as a [GapDropped] gap.
	Block = SlowPolicy{kind: policyBlock}

	// DropNewest discards the incoming record. The subscriber keeps its
	// oldest buffered records, and a [GapDropped] gap with the exact count
	// is delivered after them, before the next record it accepts.
	DropNewest = SlowPolicy{kind: policyDropNewest}

	// DropOldest discards the oldest buffered record to admit the new one.
	// A [GapDropped] gap with the exact count is delivered where the lost
	// records were, before the next record.
	DropOldest = SlowPolicy{kind: policyDropOldest}
)

// EvictAfterN behaves like [DropOldest] but closes the subscription with a
// [SlowConsumerError] on the n-th consecutive drop (one with no accepted
// record in between). n below 1 is treated as 1. Loss is exactly as for
// DropOldest until the eviction; after it, LastDelivered is a resume cursor.
func EvictAfterN(n int) SlowPolicy {
	return SlowPolicy{kind: policyEvictAfterN, n: max(n, 1)}
}

func (p SlowPolicy) String() string {
	switch p.kind {
	case policyCloseAndResume:
		return "CloseAndResume"
	case policyBlock:
		return "Block"
	case policyDropNewest:
		return "DropNewest"
	case policyDropOldest:
		return "DropOldest"
	case policyEvictAfterN:
		return fmt.Sprintf("EvictAfterN(%d)", p.n)
	default:
		return "SlowPolicy(?)"
	}
}
