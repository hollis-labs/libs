# Agent SDK

Experimental Go facade for a validated `mesh/core/agentdef` v2 definition:

```go
operative, err := sdk.New(definition, ports)
if err != nil {
    return err
}
result, err := operative.Run(ctx, sdk.Input{
    ID:          runID,
    Binding:     binding,
    Environment: environment,
    Text:        prompt,
})
```

`Ports` supplies model routes, tool implementations, authoritative host policy,
current environment/binding verification, atomic admission, approvals, events,
and optional pinned resource resolution and bounded memory retrieval. Values
are snapshotted at construction and at run admission. Port implementations and
providers remain host-owned; `New` does no I/O and starts no service.

Run the complete synthetic example from this module:

```sh
go run ./examples/headless
```

The example uses the actual `agent/core/runloop`, `turn.ExecuteTurn`, canonical
`agent/core/context`, a fake `llmcontracts.Provider`, the actual HITL service
with an in-memory store, and one fake tool. It needs no credentials and touches
no external data. It performs two model turns, pairs the tool result back to the
model, and reports usage. It is executable library evidence, not an application
integration or production host.

## Authority and admission

Hosts supply explicitly enrolled `runtimecontract.Binding` and immutable
`EnvironmentRef` values. Their shape, issuer labels, and declared capabilities
are assertions until verified against current host state. The SDK checks shape,
owner, expiry, and capability coverage; `EnvironmentVerifier` verifies actual
issuance, live lease, grants, owner and current attempt. It runs before every
model/tool admission and before memory retrieval.

`AdmissionPort.ReserveRun` atomically persists a single-use logical run ID before
any memory, model or tool activity. Its key is owner + operative + conversation +
run ID, independent of attempt/generation and provider tool-call IDs. The immutable
`RunReservation` binds the full attempt/environment, definition, selected model
route and a versioned digest of the complete input. Every subsequent effect
admission must resolve and verify that exact retained record.

Existing IDs return `ErrRunUsed`, even after failure, cancellation, unknown tool
outcome, a changed prompt, a new SDK instance or a replacement host/attempt. All
reservation errors stop execution; the SDK does not retry them. A host must retain
accepted or ambiguously committed reservations durably across crashes and must
never automatically release them. Reconciled resume is unsupported in this API.
A fresh run ID denotes a new logical input; tools are not deduplicated by their
arguments. The synthetic host uses a shared in-memory ledger to exercise these
semantics; production persistence and atomicity belong to the host.

`AdmissionPort.Admit` is the enforcement boundary. It must recheck current
binding, environment, policy, resource versions and grants, and fence changes
through effect start. Its callback runs synchronously exactly once. Models are
external/billable effects too. For tools, the host rehashes the versioned
`Operation`, checks the actual implementation and affected resources, and
atomically claims the operation before invoking the callback. Uncertain claims
stay consumed until host reconciliation. The SDK does not provide a production
claim store, authenticate principals, issue permissions, or promise exactly-once
external effects.

Policy uses `security/policy` decisions. `PDPAdapter` connects its `PDP` to
host-verified fact projection and immutable policy references:

| Decision | Execution path |
| --- | --- |
| Deny | Refuse; no tool effect. |
| Ask, or an approval obligation | Native gate request, authoritative decision verification, then host atomic admission. |
| Allow without approval obligations | Host atomic admission rechecks policy/grant provenance; no human interaction or fabricated HITL decision. |
| Evaluator error, malformed decision, unsupported obligation | Refuse; no tool effect. |

Gate and authority ports are optional for hosts whose policy authorizes operations
without approval. If a runtime decision asks for approval and either port is
absent, execution fails before the tool effect. A definition requiring
`sdk.CapabilityApproval` (`approval`) requires those ports and negotiates their
support before the first model effect.

The initial `GateAdapter` supports only `NativeAdapterScope()` and exact
once-scoped choices (`allow-once`, `deny`). It negotiates operation, decision,
scope and obligation support against the injected `gate.GatePort`, validates
its immutable ticket and resolution, and preserves the selected option/scope.
The injected `gate.AuthorityVerifier` must resolve the actual authoritative HITL
record and responder authorization; accepting the projected participant fields
is insufficient. Admission rechecks that authority while claiming the effect.

A pending result returns `ErrPending` and `Result.PendingTicket`. A cancelled or
expired wait never authorizes an effect. Cancelling the Run/Await context ends
the wait, without withdrawing the interaction. Hosts can explicitly call
`GatePort.Cancel` on the retained ticket. `Operative.Run` has no pending-run
resume. Gate delivery receipts for external native provider RPCs
belong to their adapters; a decision is never evidence of delivery or execution.

## Models, tools and context

`NewRouter(defaultName, routes...)` binds explicit route names and model names
to injected `llmcontracts.Provider` instances. `Input.Route` selects an existing
route; an empty value selects the explicit default. There is no implicit
fallback, retry or cross-provider resume. Tool runs require provider tool-calling
support. Streams must end with normalized `EventDone`; plain EOF, stream errors,
idle timeout, cancellation, and model limits fail honestly. Concrete provider
adapters are supplied by consumers, not bundled in this module.

Only exact names in `definition.Requirements.Tools` are exposed. Each registered
tool has immutable version references covering its implementation/schema and
affected resources. The host checks those references and tool arguments against
the actual implementation before effects. An optional existing `toolbroker`
selects a subset; it cannot inject tools, replace schemas, or grant authority.
Calls execute serially. Empty/duplicate call IDs and unadvertised tools reject
the complete tool turn before any of its effects.

`MCPTool` uses the shared `mcp/plugin-mcp/go-mcp/client` contract. The host owns
its authenticated connection and endpoint lifecycle. Every invocation explicitly
sets `RetryNever`, preventing the transport's default reconnection behavior from
repeating an uncertain tool effect. Text content and structured-only JSON
results are supported; non-text content is explicitly refused after receipt.
Richer result projection can be implemented as a host `Tool`.

The context window is fresh per Run. It preserves the underlying slot order,
uses the authored body plus resolved text pins, and adds scoped memory. The SDK
checks every definition pin's exact SHA-256 artifact bytes before model/tool
activity. Hosts bind opaque permission/context/approval/recovery policies in
`PolicyPort.CheckDefinition`; a pin match alone does not implement policy.
Resource reads carry the explicit binding and are host-authorized. Artifacts
are capped at 1 MiB each. Executable hooks, packaged skills and mandatory unknown
extensions are explicitly unsupported in this slice. Optional unknown extensions
remain inert. Optional `requirements.uses` are advisory; required capabilities
must be supported and present in the verified environment.

`MemoryReader` receives owner/operative/conversation/attempt scope, query,
item limit, and total serialized-response byte limit. Responses with different
bindings or owners, invalid references, or excessive size fail before model
activity. It supplies relevant context only; storage and retention stay with
the host. Static instruction and total context overflow fail instead of silently
truncating mandatory instructions or dropping tool/result pairs. Context sizing
uses the existing approximate token estimator, not a provider tokenizer.

Default limits are eight model turns, 1024 output tokens per turn, one minute
between provider events, one minute approval validity (capped by environment
expiry), four memory items / 8192 serialized bytes, and 64 KiB per tool result.
`Ports.Limits` changes these bounds. Negative limits are invalid.

## Events and failure behavior

Events reuse `harness/core/adapters/runtimeevents.Event` envelope version `1`.
The typed `EventPayload` correlates run, binding/attempt generation, environment,
definition digest, route/model, turn, tool call, operation, approval, policy and
observed execution state. It omits raw prompts, arguments, opaque native payloads,
and error strings. Run and turn terminal events include normalized token usage.
The current core turn result supplies token/cache usage, not provider cost.

Events are synchronous; sink failures stop subsequent activity and are returned.
Terminal observations use an independent context bounded to five seconds so run
cancellation can still be recorded. Hosts still choose durability and retention;
no sink can guarantee delivery when its backing store is unavailable. Each
operative has a producer-local sequencer; hosts can inject a shared `Sequencer`
when joining multiple producers. Sequence is not an attempt generation or a
durable replay cursor.

Tool transport errors and admitted callback panics have unknown external outcome.
They stop the run without retrying. A tool's known `IsError` result is returned to
the next model turn and observed as failed tool execution. A context/result limit
hit after a tool returns does not undo its effect. The host must reconcile and
retain claims before rerunning any failed/uncertain operation.

Even a run which fails preflight after reservation keeps its ID consumed.
One operative accepts one active Run; concurrent calls return `ErrBusy`. This is
not a distributed input lease. Runs carry no implicit transcript or plan across
calls. The separate [`session`](session/README.md) package coordinates fresh and
continue conversation attempts, readiness-based replacement, recovery references
and input ownership through authoritative host ports. Relaunch/fork explicitly
reject before host calls. Hosts connect its binding/lease admission to the run
admission above; no production adapter or provider-native resume is supplied.

## Trace and deterministic evaluation

[trace](trace/README.md) records typed SDK events and explicit accounting/receipts,
produces immutable JSON recordings, and replays evidence without effect ports.
[eval](eval/README.md) runs deterministic YAML scenarios through this actual loop
using synthetic models/tools/host authority, with observed trajectory grading and
presence-aware usage/cost snapshots. Production telemetry, transcript ingestion
and the richer workbench remain separate SDK-3 slices.

## Stability and checks

The initial v0 API is experimental. Additive changes preserve existing semantics;
breaking changes require a new 0.x minor and explicit migration. Neutral gate
wire version, HITL contract version and event envelope version remain independently
owned by their existing libraries. Underlying modules retain their own versions.

```sh
scripts/check -race agent/sdk
scripts/check-layout
scripts/check-one-way
scripts/scan-public
```

Run these from the repository root. Behavioral conformance includes the actual
loop, shared gate fixtures and HITL lifecycle, policy paths, receipt/capability
refusal, stale model/tool admission, scoped memory, resource pins, cancellation,
unknown-effect refusal, run-ID replay refusal across instances/replacements,
ambiguous reservation outcomes, and the shared MCP HTTP transport with a simulated
post-effect transport failure. Synthetic fixtures are not production authority.
