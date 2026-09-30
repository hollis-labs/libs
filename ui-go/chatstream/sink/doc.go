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
package sink
