package agentsessions

// AttachSnapshot describes the retained byte window captured atomically with
// an attach's replay and live subscription. Offsets count raw bytes from the
// broker's beginning, not events or write chunks. The window is half-open:
// [OldestOffset, NextOffset). NextOffset is the exclusive head (total bytes
// written). OldestOffset is the boundary immediately before the first retained
// byte, so a positive SinceSeq below it has lost history to ring eviction.
// SinceSeq zero requests the full retained ring, regardless of earlier eviction.
// An empty, never-written broker legitimately reports zero for both fields.
//
// This is a subscription-time snapshot, not a continuously current window or
// a guarantee against subsequent live subscriber drops. Unknown sessions and
// disabled brokers have no snapshot; AttachWith returns their existing error.
type AttachSnapshot struct {
	OldestOffset int64
	NextOffset   int64
}
