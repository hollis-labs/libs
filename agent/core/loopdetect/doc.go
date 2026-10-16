// Package loopdetect detects tool-call loops: the same tool called with the
// same arguments over and over inside one session.
//
// The Detector keeps a per-session sliding window of the most recent tool-call
// fingerprints. A [Fingerprint] is an FNV-1a hash over the canonical
// (tool_name, normalized_args) pair. When the same fingerprint appears at
// least the threshold number of times in the window, [Detector.Record]
// returns a [Detection]. After a detection the same fingerprint is suppressed
// (Record reports no detection for any signal while that fingerprint is still
// in the window) until it has aged out of the window, after which it can fire
// again.
//
// All state is in-memory and per session; nothing is persisted. A Detector is
// safe for concurrent use and is meant to be created once and shared across
// sessions.
//
// # Defaults
//
// [DefaultWindowSize] is 10, [DefaultThreshold] is 3 and [DefaultMaxSessions]
// is 4096. When the session cap is reached the session window that was
// created earliest is evicted.
//
// # Argument normalization
//
// Arguments are parsed as a JSON object and the top-level keys are sorted, so
// key order does not matter. Nested key order is preserved, string values are
// not case-folded, and input that is not a JSON object (an array, a scalar,
// invalid JSON) is hashed as its trimmed raw text.
//
// # Degenerate options
//
// The options are not validated, and two values are worth knowing about:
//
//   - WithThreshold(0) (or any value below 1) makes the very first Record of
//     a session fire, because a count of one already meets the threshold.
//   - WithWindowSize(0) makes the window empty after every Record, so
//     detection never fires. A negative window size panics on the first
//     Record for a new session (the window slice is allocated with a negative
//     capacity).
//
// WithMaxSessions is the exception: a non-positive value is ignored and the
// default is kept. TestWithThreshold_Zero_FiresOnFirstRecord,
// TestWithWindowSize_Zero_NeverFires and TestWithWindowSize_Negative_Panics
// pin these behaviors; they are characterization, not endorsement.
//
// # Callback
//
// A callback registered with [WithCallback] runs synchronously, under the
// detector's lock, so it must be fast and must not call back into the
// Detector.
package loopdetect
