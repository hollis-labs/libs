# Admission, cancellation and deadlines

The Go and TypeScript stdio engines enforce these connection-local limits.
Default Serve declines reverse. Explicit opt-in and validated Init acknowledgement
activate its fixed scoped clients; [reverse negotiation](reverse.md) records
accepted limits and the held child/interop merge gate.

| Resource | Limit |
| --- | ---: |
| Ordinary forward handler requests | 16 |
| Pending reverse calls | 8 |
| Lifecycle requests (Init, Load, Unload) | 2 |
| Queued frames, per writer lane | 32 |
| Queued encoded bytes, per writer lane | 8 MiB |
| Encoded host-service params or result | 1 MiB |
| Terminal reservation per admitted ID | 1 frame and 1,024 bytes |
| Consecutive eligible control frames before ordinary progress | 4 |
| Whole shutdown budget and individual write timeout | 5 seconds each |

Go `ServeOptions.AdmissionLimits` and TS `ServeOptions.admissionLimits` may
narrow handler ceilings. Accepted offer limits must also be clipped to these
local ceilings when the reverse profile is activated.

The ordinary and reply/control lanes have separate byte ceilings. Total queued
bytes may reach twice the ceiling, plus one in-progress bounded frame. The host's
aggregate limit across connections remains host policy. Admission never waits
in an unbounded handler queue. A request that cannot obtain its permit and
terminal credit fails with `rate_limited`, `effect_state: "not_started"`, before
plugin code runs. Notifications have no terminal reply.

Admission reserves terminal capacity before execution. A successful callback's
result that cannot fit the frame or queue becomes a bounded `budget_exceeded`
error with `effect_state: "committed"`. Correlation itself must fit the 1,024-byte
terminal credit before execution; otherwise the request is refused. If no
correlated bounded error can fit, the connection fails. A transport failure
never authorizes retrying an effect; mutation receipts are host-owned.

## Cancellation

`rpc/cancel` is an absent-ID notification. Its params are exactly:

```json
{"request_owner":"host","id":17,"reason":"caller_cancelled"}
```

All three fields are required, with exact case, no nulls, duplicate keys or extra
fields. `request_owner` is `host` or `plugin`. Reasons are `caller_cancelled`,
`deadline_exceeded`, `parent_cancelled`, and `connection_closing`. IDs use the
connection's domain: strings or safe decimal integers in the base protocol;
positive safe integers in the directional profile. Fractional/exponent numeric
IDs are rejected.

A peer may cancel only its own outgoing direction on this connection. Unknown
or terminal IDs and wrong-owner notifications have no effect. Invalid
notifications have no reply. An ID-bearing `rpc/cancel` is an invalid request;
it cannot cancel a handler or bypass live-ID collision checks. Cancellation
bypasses ordinary handler admission and uses the bounded control writer lane.
The reader continues processing cancellation and replies while Unload drains.

Cancellation ends a request's logical response, suppresses late output, and
cancels locally observed descendant calls without cancelling siblings. A handler
that ignores its cancellation keeps its execution permit until it actually
returns. Hook callbacks retain their enclosing request's permit even when the
hook lease wrapper has already returned. The SDK cannot kill author code.
Incoming IDs stay live until the terminal whole-write receipt; an old callback
retains its own scope and cannot reply against a reused base ID.

## Deadlines

For a forward call with `context.timeout_ms`, the relative budget starts at
complete-frame arrival before structural validation or admission. Init is
included. The callback receives the resulting cancellation signal/context.
Calls without context have **no SDK method deadline**; the host owns their
budget and may send `rpc/cancel`. No default Init, Load, command or MCP timeout
is introduced. Unload drain/cleanup/output still share the existing five-second
shutdown budget, clipped by an explicitly supplied Unload budget.

A reverse call starts its budget before validation, encoding and writer
admission. Required `ReverseContext.timeout_ms` is clipped to the caller/parent
remainder and the host's per-offer `HostServiceLimits.method_timeout_ms` entry.
There is no fallback method ceiling. Helpers must further clip caller contexts
to binding/grant expiry and validated offer limits; host authority, depth and
parent-ledger validation remain host-owned. A local parent scope is checked
against `parent_call` and any observed binding, never used as a host ledger.

At writer selection, the remaining relative timeout is encoded again so queued
time does not extend the wire budget. This preparation is bounded and cannot
increase the admitted frame size. Expired queued calls are removed before writing. Once a write may have reached
the peer, cancellation/deadline or connection failure of a mutation is
`unknown_outcome` with `effect_state: "unknown"`. No retry occurs. Cancelling a
write in progress fences the connection; one frame is never partially replaced
with another response.

## Classified base failures

Positive IDs use the existing `-32010`, `host-rpc/1` contract and leaf vocabulary.
String, zero and negative base IDs use `-32603` with exactly this data shape:

```json
{"contract":"plugin-rpc/2","code":"rate_limited","effect_state":"not_started","retryable":false}
```

The closed code enum is `rate_limited`, `cancelled`, `deadline_exceeded`,
`budget_exceeded`, or `unknown_outcome`. Effect states are `not_started`,
`not_committed`, `committed`, or `unknown`. `unknown_outcome` requires `unknown`;
`rate_limited` requires `not_started`. There is no `detail`, `request_id`, or
additional field; the outer ID correlates the reply. Classification comes from
data, not message text. Both decoders use the strict JSON scanner. Shared raw
vectors are in `protocol/v2/fixtures/duplex-control.json`.

Go `RPCTransportError.Failure` and TypeScript `RPCTransportError` retain safe
local classification. Their local causes distinguish `TransportCancelledError`
and `DeadlineExceededError` from the hook-veto cancellation sentinel. Causes are
not copied into wire errors.

## Internal helper seams

Go `scopeFromContext` and TypeScript `requestScope` retrieve immutable local
request identity and cancellation/deadline state. Go
`correlation.callContext(ctx, method, rawParams)` and TypeScript
`Correlation.call(method, rawParams, context)` admit reverse calls, register
pending correlation before publication, clip deadlines and classify transport
failures. Their `methodTimeoutMS` map is populated from the validated offer. Writer selection records
possible publication; cancellation removes queued bytes or fences active writes.
These seams are internal, not author-facing generic call helpers.
