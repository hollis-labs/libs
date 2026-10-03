// Package sink turns chatstream events into what a client understands: an
// Encoder per target, each writing Server-Sent Events. The targets are native
// (this module's own envelope), aisdk (the Vercel AI SDK UI message stream),
// agui (AG-UI), openaicompat (OpenAI-style chat.completion.chunk) and
// nanitelegacy (Nanite's chat.StreamEvent vocabulary, for migration).
//
// An Encoder is per subscription and stateful: it keeps the id maps and step
// tracking its target needs, so feed one Encoder the events of one run, in
// order, and always the same Writer. SSE bytes are produced by go-ssekit's
// Writer (through Bind), not by hand: one flush per frame, every write and flush
// error returned to the caller, LF line endings, multi-line data split
// correctly.
//
// Resume: an encoder is fed a run from its first event (run.start), not a tail.
// A subscription resumed with Last-Event-ID delivers only the events after the
// cursor, and a fresh Encoder cannot know which parts and steps those events
// belong to. A resumed subscription must therefore replay the run from its first
// event into a fresh Encoder (and skip writing the frames the client already
// has), or keep the Encoder alive across the reconnect. An encoder never guesses:
// an event for a part or step it has not seen open (a part.delta or part.end
// with no part.start, a part opened twice, a step.finish for an unopened step)
// is refused with an error wrapping ErrOutOfOrder and writes nothing. A tail
// that happens to begin with an event that needs no earlier state (a run.finish,
// a usage event) is accepted, and the output then lacks what the missing prefix
// would have written.
//
// Every encoder keeps the same contract:
//
//   - Encode after the terminal event returns an error wrapping
//     chatstream.ErrAfterTerminal and writes nothing.
//   - Close writes the target's terminator. If the run had not ended it first
//     writes the target's error representation, so a client is never left
//     hanging without a signal, with the cause in the message. Close is
//     idempotent; Encode after Close returns ErrClosed.
//   - Verbs an encoder does not know are skipped: the schema is additive.
//   - A write or flush error is returned from Encode or Close and is sticky.
//   - No event, however out of order, panics an encoder; an event that does not
//     follow from the run so far is an ErrOutOfOrder error (see above).
package sink
