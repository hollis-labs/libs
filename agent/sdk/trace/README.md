# SDK evidence recording

`trace` is an experimental, passive evidence API inside `agent/sdk`. It reuses
`runtimeevents.Event` envelope version `1` and the existing neutral binding,
reference and gate receipt types. It creates no approval store, telemetry
exporter, runtime controller or effect executor. Recording version `1` is
experimental and independent of event, gate and module versions.

```go
recorder, err := trace.NewRecorder(trace.Metadata{Build: buildRef})
if err != nil {
    return err
}
ports.Events = recorder
operative, err := sdk.New(definition, ports)
if err != nil {
    return err
}
_, runErr := operative.Run(ctx, input)
// Hosts can explicitly report delivery/execution receipts to recorder as well.
// Preserve the snapshot even when runErr reports an uncertain effect.
recording, err := recorder.Snapshot()
if err != nil {
    return err
}
entries, err := trace.Replay(ctx, recording)
// runErr and replay errors remain separate from the recorded evidence.
```

`Recording` contains private serialized bytes. `Entries`, `Metadata`, JSON output
and replay all return detached copies. A recording can be persisted with standard
JSON encoding and restored with JSON decoding; imports validate the envelope,
correlations, references, receipt discriminants and explicit usage rows. The
caller owns persistence, integrity, retention and access control. Shape validation
is not authentication of an imported observation or its issuer.

A recorder accepts at most 10,000 entries. SDK payloads must match this slice's
known `sdk.EventPayload` fields. Unrecognized payload fields and unsupported
recording versions fail explicitly rather than being silently discarded.

## Correlation and coordinates

Each entry carries a typed `Correlation`: logical run, full binding with attempt
and generation, environment receipt, definition digest, route/model, model-call
ID, turn/tool-call IDs, operation, approval and decision IDs where observed.
Optional snapshot/build references are supplied once in recording metadata;
they are evidence locators, not restore, deployment or execution authority.
Snapshot owner must match the observed binding. Build references can identify
shared artifacts under a different issuer/owner.

The SDK now emits tool names and distinct `ModelCallID` values. The current loop
performs one model invocation per turn; its ID is derived as `<turn>/model/1`.
That is an SDK correlation ID, not an OpenTelemetry span ID or provider session.

`Position` preserves four independent domains:

| Field | Source |
| --- | --- |
| `SourceSequence` | Producer-local `Event.Sequence`. |
| `ProducerGeneration` | Producer restart generation; zero stays unknown. |
| `PTYOffset` | Optional observed raw byte offset; pointer-to-zero is retained. |
| `CommittedCursor` | Optional durable-log cursor, supplied by its owner. |

Binding attempt generation is separate from all four. Recording/replay never
manufactures a cursor from sequence or bytes, sorts by those values, or retargets
an older binding to a successor. Supplemental receipts/usage have no fabricated
source sequence or durable position.

## Decision, delivery, effect and accounting

Approval-request/resolution events are observations of the authoritative gate
lifecycle. They carry decision references when resolved, not a new decision
record. `RecordDelivery` and `RecordExecution` accept independently reported gate
receipts and attach them only to exactly matching observed correlations.

A decision does not imply native delivery. Headless SDK evaluation normally has
no native delivery receipt. Sent, acknowledged, rejected and unknown remain
distinct; unknown delivery/effects remain unknown in replay. Successful delivery
never implies successful execution. Local-policy execution can reference policy
authority without fabricating a human decision.

`RecordUsage` accepts one final `usageledger.Row` per observed model call. This
explicit adapter report preserves component provenance, measured zero versus
unreported, immutable price snapshots, unknown rates and cost kind. It is not a
side channel in `StreamEvent.Content`. The SDK's legacy usage fields remain
available as observations, but cannot establish count presence or reasoning
subdivisions. Run/turn usage summaries are not additional billable model calls.

## First-slice trace / GenAI mapping

The existing envelope's `Corr.TraceID`, `SpanID` and `Traceparent` are preserved
when supplied by a host. They are not synthesized from run or tool IDs. This
package does not create spans, install a tracer provider, instrument production
providers, or export OTLP.

The repository's [existing GenAI helpers](../../../core/util/otel/genai/attributes.go)
provide the compatible attribute vocabulary for a later host telemetry adapter:

| Existing key | Evidence available here |
| --- | --- |
| `gen_ai.system` | Explicit usage-row provider, when reported. |
| `gen_ai.request.model` | Correlated model identifier. |
| `gen_ai.operation.name` | Model-turn lifecycle can map to `chat`. |
| `gen_ai.usage.input_tokens` | Disjoint input/cache components, under an explicit provider mapping. |
| `gen_ai.usage.output_tokens` | Output/reasoning components, under an explicit provider mapping. |
| `gen_ai.response.finish_reason` | SDK usage observation's reported stop reason, when present. |

An adapter must preserve unknown provenance, omit unsupported measurements and
choose the provider's inclusive/exclusive count semantics. Summing an unknown
component as measured zero would overstate evidence. Tool/approval/environment
references stay SDK evidence; no production semantic-convention coverage is
claimed by this mapping. Routine SDK events omit prompts, completions, arguments,
opaque native payloads and raw error strings.

## Replay boundary

`Replay(ctx, recording) ([]Entry, error)` validates and returns evidence in its
recorded order. It accepts no sink, callbacks, model, tool, runtime, gate or
admission ports. It cannot make live calls or resume an SDK run. Returned entries
remain observations, never executable instructions or permission to route an old
approval to a replacement attempt. An unknown outcome requires host reconciliation;
replaying its record supplies none.

External transcript ingestion, provider-specific replay adapters, durable stores,
production OpenTelemetry instrumentation, inspectors and workbench UI are later
SDK-3 slices.
