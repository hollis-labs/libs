// Package chat is a conversation-scoped chat wire: one HTTP and SSE handler,
// built once, that any host mounts.
//
// A conversation outlives any single turn and any single client. A client can
// submit a turn, attach as a viewer or a controller from a cursor, take or hand
// over the right to type, detach and cancel. Events on the wire are
// chatstream/v1, unchanged, carried through one streamhub stream per
// conversation that does not end when a run does.
//
// # Layers
//
//	client ──HTTP/SSE──▶ Handler ──▶ Service ──▶ Runner
//	                                   │
//	                                   ├─ streamhub (conversation event log)
//	                                   └─ Control (attachments, lease, generation)
//
// A [Runner] runs one turn and emits chatstream events; it is the main thing a
// host implements. A [Service] owns the conversation: it checks the lease,
// starts a run, publishes the runner's events into the conversation stream with
// the user's message first, and answers approvals. [NewHandler] is HTTP and SSE
// over the Service.
//
// Everything a host varies is injected through [Config] and [NewHandler]: the
// [Authorize] check, the [Runner], the event log (a streamhub.Log), the
// [Control] store, the [Grants] store, the [Intercept] hook, the [Clock] and the
// ID source. The sqlitestore sub-package is a durable log and Control in one
// SQLite file, memstore is the in-memory Control, and chattest holds the
// conformance suites and a scripted runner.
//
// # What is not here
//
// Authentication belongs to the host's server. Model history (tool calls,
// thinking, compaction) belongs to the runner; the conversation stream is what
// clients see, not what the model is sent. A turn's Context is passed to the
// runner and never published or logged.
package chat
