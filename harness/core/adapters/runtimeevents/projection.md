# Envelope v1.1 and the mesh.Event projection

`runtimeevents.Event` and `mesh.Event` (module `mesh/core`) are two envelopes for one event stream.
There are two envelopes, and a lossless projection between them, by design (event stream contract, ruling D3).
`ToMesh` and `FromMesh` convert one into the other.
Both envelopes stay at `schema_version` "1". Every v1.1 field is optional and additive.

The design and its rulings are in Tesseract: `project/portfolio-architect/workspace/design` / `event_message_stream_contract_2026_10_16`, sections 4 and 16.

## Rules the envelope encodes

- **D1, parent and cause.** `parent_id` is a structural link to another event from the same producer, such as a response to its request or a tool result to its call. `corr.causation_id` is a causal link to the event, from any producer, that made this one happen. Both may be set. A reaction effect sets `causation_id` to its trigger and `corr.depth` to the trigger's depth plus one.
- **D2, session or subject.** `session_id` is required for session-bound kinds (`process.`, `session.`, `turn.`, `stdin.`, `stdout.`, `stderr.`, `agent.`, `policy.`, `plant.`, `sandbox.`, `interrupt.`). Every other event carries `subject`, a URN such as `msg://task/torque/CW-20261016-0129`. Every event has at least one of the two. `session_id` is now omitted when empty.
- **D6, identity and order.** `id` is minted once by the producer with `NewEventID` and is the dedupe key. Retries reuse it. `sequence` and `generation` are the producer's own counters and are not comparable across producers. `cursor` and `recorded_at` are assigned by the durable log, and `cursor` is the only value a consumer resumes from. A log cursor such as `<env>:<seq>` is never used as an `id`.

## Field table

| runtimeevents | mesh.Event | Notes |
|---|---|---|
| `schema_version`, `id`, `kind`, `time`, `app`, `session_id`, `turn_id`, `parent_id`, `raw_offset` | same names | |
| `sequence` | `source_sequence` | |
| `generation` | `generation` | Producer restart counter. Zero means unknown. |
| `process` (struct) | `process` (raw JSON) | An empty process is omitted from mesh. |
| `source.channel` | `source.channel` | |
| `source.confidence` exact / derived / inferred | `source.confidence` 1 / 0.5 / 0.25 | See the limits below. |
| `subject` | `subject` | |
| `cursor`, `recorded_at` | `cursor`, `recorded_at` | Set by the log only. |
| `content_type`, `truncated`, `payload` | same names | |
| `payload_digest` | `payload_digest` | Retention tombstone (D7). The payload is absent here and `null` on mesh. |
| `scope` | `scope` | Same Go type (`mesh.EventScope`). |
| `corr.trace_id`, `corr.span_id`, `corr.traceparent` | `trace.trace_id`, `trace.span_id`, `trace.traceparent` | |
| `corr.causation_id`, `corr.correlation_id`, `corr.thread_id`, `corr.in_reply_to`, `corr.idempotency_key` | top-level fields with the same names | |
| `corr.depth` | `depth` | |
| `actor.urn`, `actor.kind` | `actor.urn`, `actor.kind` | Mesh vocabulary: agent, user, service, tool. |
| `actor.role`, `actor.verified` | `actor_role`, `actor_verified` | |
| `actor.on_behalf_of` | `on_behalf_of` | |
| `class.visibility`, `class.payload_schema` | `visibility`, `payload_schema` | |
| `class.category`, `severity`, `sensitivity`, `pii`, `labels`, `sanitized`, `redactions` | `class.*` with the same names | |

## Limits of losslessness

- **runtimeevents to mesh and back is exact** for every field, with two exceptions. An empty block (`"corr":{}`) comes back absent. A confidence string outside exact, derived and inferred comes back empty.
- **mesh to runtimeevents and back is exact** for every mesh value that `ToMesh` can produce, with three exceptions:
  - A confidence number outside the table maps to the nearest lower grade.
  - Keys inside `process` that runtimeevents does not define are dropped.
  - A JSON `null` payload becomes absent.
- `ToMesh` adds nothing. An event without `actor`, `subject`, `cursor` or `generation` becomes a `mesh.Event` that `mesh.Event.Validate` rejects until the hub stamps those fields.

## Conformance

- `schema/event.v1.schema.json` (embedded as `JSONSchema`) mirrors `Event.Validate`. Like `Validate`, it accepts unknown kinds, fields and vocabulary values.
- `testdata/conformance` holds fixtures that implementations in other languages can reuse:
  - `valid/`: must pass the schema and `Validate`, and must survive runtimeevents to mesh and back unchanged.
  - `invalid/`: must be rejected by both the schema and `Validate`.
  - `mesh/`: must survive mesh to runtimeevents and back unchanged.
