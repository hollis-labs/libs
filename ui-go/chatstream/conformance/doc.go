// Package conformance is the test kit that ships with go-chatstream: a
// validator for event streams, the reducer oracle, and helpers that check a
// Decoder against recorded upstream streams.
//
// Validate checks the invariants every stream must keep, whichever dialect it
// was decoded from: exactly one terminal event and nothing after it, parts and
// messages and steps opened before they are used and closed before the run
// ends, no text on a tool call or JSON fragments on text, valid UTF-8, a closed
// finish-reason vocabulary, at most one final usage, cumulative usage that never
// goes down. CheckReplayEquivalence is the reducer oracle: reducing a stream
// from any cursor, starting from the snapshot at that cursor, equals reducing
// it whole.
//
// A decoder's own test file calls CheckDecoder for each recorded stream
// (frames with inter-chunk timing, and the golden events they must produce) and
// CheckTruncation for the failure every dialect must survive: the upstream
// ending without its terminal signal. Fixtures are plain JSON files, so a new
// capture is a new file, not new code.
package conformance
