// Package pluginhooks provides a dependency-free, host-owned hook catalog,
// generation-scoped registration registry and bounded execution engine.
// Hosts create Registry and Scope; plugins receive only their scope capability.
// Declarations require explicit modes, schema validators, limits and error policy.
//
// Engine executes sequential actions, parallel observations, bail gates,
// waterfall filters, bounded async work and one-shot post-commit observations.
// Every handler receives private JSON bytes and copied metadata. Views project
// declared JSON Pointer paths; filters can change only visible mutable paths,
// and merges retain hidden fields. Hosts provide bounded compiled validators.
//
// Invocation timeouts release callers, but execution capacity remains held until
// the handler actually finishes. Context cancellation cannot kill uncooperative
// Go code or reverse external effects. Once is claimed atomically after capacity
// acquisition. Removal and disposal cancel queued/waiting/in-flight work and
// invalidate late results. No plugin code executes under registry locks.
//
// Async receipt means queued, not completed; Future.Await reports eventual results.
// PrepareAfterCommit produces a host-owned Commit/Rollback capability. Only the
// host can confirm a transaction, and scheduling is in-memory rather than durable.
// Nested dispatch carries depth through context, including detached async work.
// Context propagation is cooperative, not an isolation boundary for hostile code.
//
// Engine adds generation-scoped breakers and value-only telemetry seams. Host
// sinks receive sanitized outcomes and transitions outside core locks. Identity,
// authorization, transport, schema compilation, telemetry export and durable
// delivery belong to host integrations or separately scoped work. This
// module has no dependency on plugin-sdk or go-hooks and provides no legacy aliases.
package pluginhooks
