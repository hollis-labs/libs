# Single-call remote hook seam

The core remains standard-library-only and imports no SDK or wire package.
A trusted host implements `RemoteHandler.Handle(context.Context, RemoteRequest)`
over its own transport. A host adapter or external bridge importing both modules
maps the seam to negotiated wire DTOs; no adapter is embedded here. This API is
not a JSON-RPC encoder, connection negotiator, authorization policy or transport.

## Catalog document change

`Definition.RemoteLatencyBudget` appears in introspection as
`remote_latency_budget_ms`. It must be positive for `remote_ok=true` and no greater
than `handler_timeout_ms`, which must remain within `budget_ms`. Remote-disabled
declarations use zero/omission. The host chooses a round-trip ceiling and keeps
streaming/high-frequency declarations `remote_ok=false`; the library does not
infer frequency from hook names. Every remote filter consumes a round trip.

Two additional catalog policy fields reserve the remaining remote surface:

| Go field | JSON field | Validation |
| --- | --- | --- |
| RemoteBatchMax | remote_batch_max | Zero/omitted means the default cap 64; explicit 1..64 only lowers it. Nonzero requires remote observation action (sequential/parallel/async/after_commit), never filter or bail. |
| RemoteFireAndForget | remote_fire_and_forget | Default false. True requires remote action with async/after_commit mode. |

All three fields require remote_ok when nonzero/true. The single-call seam does
not implement batching or notifications yet; catalog intent does not advertise
transport support. Requests with responses remain the only execution path here.

`CatalogDocument` has **no independent document-format version**. Its
`catalog_version` belongs to the publisher's declarations and is independent of
module, document shape and transport-profile versions. This change accepts new
optional policy keys and makes the latency field mandatory for remote-enabled
declarations. An older closed-key TS reader rejects a document with these new
fields; this PR updates Go introspection, TS reader and shared fixtures together.
Existing remote-enabled declarations must supply the latency budget. There are
no tags or adopters requiring a compatibility path. Local declarations still
omit all three zero/default fields. Discovery and generated markdown use this
same updated document.

## Host registration

Create a fresh `RemoteConnection` only after the host's required profile and
identity have been negotiated. It is a host-owned lifetime fence, not a bearer
grant or another generation counter. `Close()` is terminal. The host disposes
the old scope and uses a fresh connection and generation when replacing a plugin.
A transport may keep connection-specific state; the bridge must check its
negotiated incarnation and registration/digest against each admitted request.

`AddRemoteAction`/`AddRemoteFilter` require both a remote-enabled declaration and
`ScopeConfig.Remote=true`. Local scopes can attach in-process handlers only;
remote scopes can attach `RemoteHandler` registrations only. Registration names,
allowlists, required views, digests, resource caps and deprecation still use the
same registry policy. `ValidateRegistration` is policy preflight, not a transport
attachment or name reservation.

```go
connection, err := pluginhooks.NewRemoteConnection()
if err != nil { return err }
// The host calls connection.Close() on transport loss and scope.Dispose(ctx)
// on unload; reload receives a fresh host-assigned generation.
_, err = scope.AddRemoteFilter("document.rendering", "render", pluginhooks.Options{
    View: "public", SchemaDigest: "render-v1",
}, pluginhooks.RemoteRegistration{
    Connection: connection,
    Handler: hostTransport,
    LatencyEstimate: 5 * time.Millisecond,
})
```

`hostTransport` implements the interface. It must remain responsive to control
and replies while a request is pending, honor context cancellation and avoid
retaining/mutating its privately owned returned result. The host owns the estimate;
it is positive and cannot exceed the catalog ceiling. The engine checks it again
against the remaining individual lease before capacity/once admission and send.

The remote deadline is the earliest caller/dispatch deadline, registration limit
and remote ceiling. Projection/encoding, capacity waits, payload copies, writer
queues, round trip and output validation consume the same budget. A latency refusal
sends nothing and does not consume once. Timeouts release the caller, while permits
remain held until the adapter actually returns. A host must not silently retry an
unknown action outcome: its external effects may already have happened.

## Request snapshots and fences

Each call receives a unique handler invocation ID; catalog version/name/digest,
kind/mode; host instance, opaque generation and registration identity; random
256-bit binding and connection references; deadline and positive remaining
individual/dispatch budget snapshots; trace and informational depth; privately
owned projected payload and string metadata (empty map when omitted).

These are transport-agnostic seam types without wire JSON tags. In particular,
the registry generation remains a string. A bridge maps its host-verified wire
incarnation without deriving new authority from a string or a namespace. Request
fields are private copies: changing one cannot alter the retained deadline,
registry identity, full payload or expected response correlation ID. Go contexts
remain authoritative; absolute timestamps do not assume synchronized clocks.

Responses must echo the exact invocation ID. Scope removal/disposal and connection
closure cancel waiting/in-flight calls and prevent acceptance of late output.
Fresh registrations get fresh opaque handles; stale handles cannot remove another
scope's replacement. The full accepted filter value still passes schema validation,
payload byte limits and visible/mutable merge rules; hidden fields remain intact.

Reverse callback binding resolution, shared ancestry budgets and cycle rejection
are a following implementation. The single-call seam does not provide reverse
callback authorization. Informational depth/trace in a request grants no permission
and cannot override the engine's cooperative context guard.

## Structured per-handler results

| Status | Required/allowed shape |
| --- | --- |
| ok | Filter requires JSON payload (including explicit JSON null if schema permits); action forbids payload. No failure or reason. |
| cancelled / approval_required | Bail action only; optional diagnostic reason, no payload or failure. |
| failed / unavailable | Required closed failure code, optional diagnostic message, no payload or reason. |

Only the validated deliberate statuses reconstruct `ErrCancelled` and
`ErrApprovalRequired`, preserving `errors.Is`. Text, failure messages, legacy
sentinels or numeric transport errors cannot become a veto. Operational cancellation
is a failure with `caller_cancelled`, not deliberate cancelled. Unknown statuses,
unknown codes, correlation mismatches and contradictory shapes are invalid output.

The failure vocabulary is `remote_not_allowed`, `latency_budget_exceeded`,
`stale_scope`, `stale_binding`, `capacity_exhausted`, `deadline_exceeded`,
`caller_cancelled`, `depth_exceeded`, `callback_cycle`, `transport_failure`,
`handler_panic`, `invalid_output`, `handler_error`, `schema_mismatch`, and
`profile_unavailable`. `RemoteFailure` preserves a validated code; `Error()` emits
only that code. Diagnostic text never enters telemetry or permission decisions.
A non-nil error returned by Handle means transport failure and wraps `ErrTransport`,
even if the adapter returns a veto sentinel. Panics are contained by the normal
handler boundary.

The host aggregates per-handler results into the engine's dispatch status under
the registration's existing error policy. An open failed filter preserves the
previous value; a closed gate remains blocked. Transport errors, timeout, panic
and invalid output count in the generation breaker. Host admission refusal,
caller/unload/connection cancellation and deliberate bail outcomes are excluded.
Open breakers skip transport and report unavailable. One half-open probe can run;
operator reset retains stuck permits. A live remote endpoint reporting an ordinary
unavailable/caller-cancelled failure cannot impersonate host cancellation to avoid
failure accounting.

## Conformance

The stdlib `hookstest` suite appends R16 (policy/latency), R17 (structured results
and vetoes), R18 (identity/copy/connection/unload fences), and R22 (remote breaker).
R01-R15 are unchanged; R19-R21 are reserved for callback ancestry, batches and
notifications in the next change. Tests run against an in-memory fake transport,
with a broken-adapter proof for each new requirement and zero reference waivers.
The TS twin validates the same catalog policies and refuses in-process callbacks
in remote scopes; its remote execution adapter is not implemented here.
