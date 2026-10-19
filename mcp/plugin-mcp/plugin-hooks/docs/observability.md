# Observability and owner-generation breakers

Configure `ExecutionConfig.Sink` to receive completed dispatch records, terminal
handler records and breaker transitions. A nil sink is a no-op; breaker behavior
is always active. The [runnable example](../examples/observability/main.go) wires a
concurrency-safe JSON sink, opens a breaker, resets it and disposes its generation:

```sh
go run ./examples/observability
```

## State and accounting

The breaker key is `(registry.HostInstance(), owner, generation)`. A fresh
registry has a random process epoch, and reload needs a fresh host-issued
generation. All registrations in that generation share a breaker, across hooks.
Defaults are five consecutive terminal failures and a 30-second cooldown; set
`ExecutionConfig.Breaker` to override them. Zero selects defaults, negative
configuration fails. The state machine is `closed` → `open` → `half_open` →
`closed` on successful probe, or back to `open` on failure. Disposal is terminal.

An open breaker skips attempts with `ErrUnavailable` under the registration's
existing error policy. An open filter retains its previous accepted value;
a closed gate stays blocked. Registrations are retained and once is not consumed
by a skipped attempt. After cooldown, exactly one half-open probe is admitted
across the entire generation. Its normal capacity, deadlines, schema validation
and policy still apply. A failed probe opens for another cooldown. An excluded
probe releases the probe reservation while staying half-open, allowing a later
attempt without fabricating success.

Count ordinary/transport errors, panic, invalid filter output and individual
handler timeout. Transport adapters may wrap `ErrTransport` for an explicit
`transport_error` class. Deliberate `ErrCancelled`, `ErrApprovalRequired` and
`ErrDepthExceeded` do not count. Caller/whole-dispatch cancellation, unload/removal
and admission without an actual invocation also do not count. These exclusions
use actual lifecycle/context state: a running handler returning canceled or
unavailable with live contexts is an ordinary `handler_error`, preserving its
original error sentinel in the result.

Each attempted call has one terminal accounting decision. A handler timeout
counts once even while uncooperative code retains its execution permits; its late
result never changes that decision. Filter outputs are validated before lifecycle
completion and account as success only after validation. Counters serialize in
terminal-observation order, independent of ordered parallel outcome collection.
Successful calls reset the consecutive count only in their current accounting
epoch. Opening, manual reset, successful probe and disposal invalidate older
calls, so prior in-flight success cannot close an open breaker and late failures
cannot reopen a manually reset generation.

Trusted hosts inspect `engine.Breaker(owner, generation)` or `engine.Breakers()`
and call `engine.ResetBreaker(owner, generation)`. Snapshots include state,
consecutive failures, last failure class/time, cooldown end and active probe.
Reset clears counters/probe/cooldown, preserves last-failure history and starts a
fresh accounting epoch. It neither cancels running code nor releases stuck
permits. Unknown generations fail; disposed ones cannot be reset. `Scope.Dispose`
marks the breaker disposed even if waiting for in-flight code times out. New
generations do not inherit quarantine. Persisted quarantine and reset permissions
belong to the host, and this module mounts no operator HTTP endpoint.

## Clock and sink boundaries

An injected `Clock.Now()` controls cooldown and telemetry timestamps; it must be
bounded, monotonic and concurrency-safe. Context-based execution deadlines keep
using real timers. Tests advance the clock without sleeping through cooldowns.
Clock and sink callbacks run outside core locks. Sink callbacks must be bounded,
concurrency-safe and avoid recursive dispatch. They can query engine state.
Panics are recovered without changing permission/dispatch behavior and increment
`engine.TelemetryFailures()` for the host to monitor. Blocking or lossy exporters
need their own bounded queue and drop/failure counters; the engine creates no
unbounded telemetry goroutines.

Every transition and manual reset emits `BreakerEvent`. Callbacks may arrive out
of order; use the generation-scoped `Snapshot.Sequence` to reconcile state. All
records are copied values with no mutable maps, slices, error objects or payloads.
Only safe classifications are exported, never handler/validator/panic text or
arbitrary invocation metadata. Hosts must supply non-secret identity/catalog
labels; putting a secret into an owner or declaration name makes it an identifier.

## Traces and metrics

`WithTraceContext` accepts host-verified nonzero lowercase hexadecimal trace and
parent-span IDs. `TraceContextFrom` exposes the parent installed in handler
contexts. Each dispatch and handler has its own span ID; nested calls share the
trace, point to the invoking handler span and increase depth. Detached async and
post-commit work retain trace/depth. Failed preparation, depth rejection, queue
refusal and rollback also produce dispatch records. Receipt is not completion:
async dispatch records are emitted after execution, not when queued.

`TraceRecord` carries the following stable JSON fields. Handler-only identity and
policy fields are empty on dispatch records because a dispatch can span owners.

| Fields | Meaning |
|---|---|
| `stage`, `trace_id`, `span_id`, `parent_span_id`, `invocation_id` | Dispatch/handler correlation |
| `hook_name`, `catalog_version`, `schema_digest`, `host_instance` | Declaration and registry epoch |
| `owner_id`, `owner_generation`, `registration_name`, `handle_id` | Generation-qualified handler identity |
| `priority`, `registration_sequence`, `mode`, `depth`, `transport` | Ordering and execution context; transport is host-scoped local/remote |
| `started_at`, `ended_at`, `queue_ms`, `duration_ms`, `effective_timeout_ms` | Start/terminal timestamps, queue/capacity delay and wall time in milliseconds |
| `on_error`, `outcome`, `error_class`, `breaker_state` | Resolved policy and sanitized terminal state |
| `started`, `handler_count` | On handler records: whether plugin code began; on dispatch records: number of collected attempt outcomes |

Duration includes queue/capacity wait. Handler queue time ends at actual start;
dispatch queue time includes preparation and async/post-commit waiting until
execution begins. A timed-out handler record ends when the caller is released,
not when abandoned code eventually returns. Aggregate per-hook count/outcome and
duration from dispatch records; derive attempted/started/skipped handler metrics
from handler records. Use low-cardinality hook/mode/outcome labels for metrics;
put invocation IDs and generation-qualified identity in traces/operator records.

## Host-side OpenTelemetry bridge

OpenTelemetry imports belong in the host adapter. The core's trace IDs, parent
IDs, timestamps and value attributes can be mapped to SDK spans. To preserve the
engine span IDs, use a dedicated bridge provider whose
[IDGenerator](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/trace#IDGenerator)
reads the current `TraceRecord` from adapter context. Use
[WithTimestamp](https://pkg.go.dev/go.opentelemetry.io/otel/trace#WithTimestamp)
for both start and end. This is a host integration blueprint, not a compiled
adapter or dependency in this module:

```go
// Host-owned SDK setup. hookRecordIDs implements sdktrace.IDGenerator:
// NewIDs returns record.TraceID/SpanID; NewSpanID returns record.SpanID.
// It is used only on this bridge provider, with validated record context.
provider := sdktrace.NewTracerProvider(
    sdktrace.WithIDGenerator(hookRecordIDs{}),
    sdktrace.WithBatcher(hostExporter),
)
bridgeTracer := provider.Tracer("plugin-hooks")

// Inside hostSink.Record(r): validate/parse r's hex IDs, put r in adapter
// context, and set the parent span context from TraceID + ParentSpanID.
_, span := bridgeTracer.Start(recordContext, r.Stage+" "+r.Hook,
    trace.WithTimestamp(r.StartedAt))
span.SetAttributes(
    attribute.String("hook.name", r.Hook),
    attribute.String("hook.outcome", r.Outcome),
    attribute.String("hook.error_class", r.ErrorClass),
)
if r.ErrorClass != "success" {
    span.SetStatus(codes.Error, r.ErrorClass) // Classification only.
}
span.End(trace.WithTimestamp(r.EndedAt))
```

The adapter supplies ID parsing, parent sampling, a bounded export queue, metrics
and shutdown. Do not attach invocation metadata, payloads or arbitrary error
messages. Breaker transitions become generation-scoped operator events; use their
sequence to handle out-of-order delivery. No tracing backend or application is
adopted here.
