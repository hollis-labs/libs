// Package mesh defines portable contracts for agent providers: actor addresses,
// verbs, task and session states, live capability descriptors, negotiation,
// commands and responses, and a shared event envelope.
//
// Providers implement Provider using their own execution and messaging systems.
// Consumers negotiate exact capability versions and branch on advertised support.
// InstanceView describes an execution; it is independent of per-task TaskState.
// The dispatch capability adds Assign receipts, TaskLookup and EventFollow.
// Assign requires a caller-scoped IdempotencyKey and immutable work content
// (CorrelationID is trace metadata and excluded from identity):
// the same key/content recovers the original task and selected team member;
// changed content conflicts. Successful admission is not readiness or completion.
// Lookup reads current task/result facts and an atomic log watermark. Follow
// pages after opaque cursors and may wait at the head; a replay gap requires
// snapshot reconciliation before dependent transitions continue. Follow grants
// and per-event visibility are enforced by the authoritative host.
// Task reads and controls are scoped to the caller/assignee (or an explicit
// operation grant); unauthorized access is masked as not_found. Parent lineage
// requires authority over the parent task. Queued work cannot report a result
// before delivery, and delivery does not erase an intervening wait state.
// ReportResult requires a VersionedResult with a supported schema/media type
// and verified content digest. Unknown schemas cannot complete work. The content
// is encoded as bytes so JSON transport preserves the exact digest input.
// Production hosts retain keys through pending work and its recovery window,
// authenticate actors and delegated authority, and commit receipt/task/delivery
// atomically before acknowledging. Call cancellation only stops the call; it
// never implicitly cancels admitted work or authorizes a replacement assignment.
// The root package depends only on the Go standard library.
//
// The fake subpackage implements an in-memory provider for host tests without
// launching processes. The conformance subpackage checks the MVP verbs and both
// directions of capability claims, with a separate RunDispatch suite. Unsupported vocabulary verbs must return a
// typed ErrorUnsupported rather than silently falling back.
package mesh
