// Package chattest is the acceptance kit for hosts of the chat wire.
//
// [RunnerConformance] checks any chat.Runner against the runner rules, and
// [ControlConformance] checks any chat.Control against the lease rules. Both
// hosts of the wire must pass them.
//
// [Scripted] is a runner that plays back a list of events: text with deltas,
// reasoning, a tool call and its result, an approval that blocks until it is
// answered, each kind of ending, and a whole-message mode with no deltas. A
// client can be built and verified against the real handler with it before
// any real runner exists.
//
// [Clock] is a manual chat.Clock and [IDs] a predictable ID source, so tests
// of expiry never wait on real time.
package chattest
