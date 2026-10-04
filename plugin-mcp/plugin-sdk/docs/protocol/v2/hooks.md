# Hooks/1 profile

The SDK routes `hook/handle` and `hook/handle_batch` in production after
connection-local Init negotiation. The host offers
`hooks_profile: { "hooks_profile_version": 1 }`; the plugin opts in by implementing
Go `HookHandler` or TypeScript `hookHandle`. After successful Init, Serve
acknowledges `hooks_profile_version: 1` and enables that connection's hook methods.
The runtime owns the acknowledgement: authoring that result field, package
versions or the presence of DTOs cannot enable hooks. There is no public enable
switch. Await the Init reply before sending hook requests.

A valid offer without a handler, or a handler without an offer, completes Init
without a hooks acknowledgement. Hook calls on those initialized connections
receive -32601 with `hooks/1` and `profile_unavailable`. An offer with a version
other than 1 retains the strict typed Init rejection, -32602 `profile_mismatch`;
malformed, null or unknown fields retain existing Init errors. Before successful
Init, base lifecycle admission rules apply. The internal conformance seam remains
for tests that need to bypass negotiation; negotiated transcripts use normal Init.

Hooks are acknowledged independently of reverse RPC. Reverse acknowledgement
requires its own explicit Serve opt-in and valid host_services offer. Hook
handlers receive the ordinary context and no host client, so this profile
supplies no plugin-originated callbacks. Hook-originated reverse calls remain
unavailable even with both acknowledgements;
SDK hook-context and plugin-hooks adapter maintainers own a separate per-item scope/client design. See
[reverse negotiation](reverse.md) for the child/interop merge gate.

`hooks.schema.json` defines flat DTOs. `generate.py` generates a separate
`hooks-wire.ts`; existing wire.ts and historical v1 files do not change. The
`x-raw-preserve: true` annotation emits unknown and a preservation comment. Go
uses json.RawMessage. TypeScript decodes author input as HookRequest, containing
both parsed `payload` and branded `payloadJSON`. The `rawJSON(text)` constructor
uses the shared strict scanner; both runtimes reject duplicates, invalid Unicode
and nonfinite JSON numbers before parsing. Opaque payloads can contain null,
fractions and large integer literals; structural integers use decimal tokens and
bounded portable ranges. TS object keys come from scanner tokens, including
successive escaped-key payloads.

All structural objects are closed, exact-case and non-null. Hook params require
invocation_id, catalog_version, hook, schema_digest, kind, mode, scope, context,
payload (including explicit null), metadata (including {}), deadline,
aggregate_budget_ms, depth, trace and root_invocation_id. parent_invocation_id
is optional and cannot be null. Hook names follow
`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`. Scope contains the canonical incarnation
and registration_id. Forward context contains only binding_id? and timeout_ms;
ancestry, deadline, depth and trace remain top-level hook fields. The SDK compares
the scope incarnation with Init before author code; hosts verify catalog/digest,
registration, notification opt-in and the live binding ledger. These DTOs confer
no authority.

Requests require a positive safe integer id and context.binding_id. A notification
has id absent: null, zero or a string id never becomes a hook notification. Only
action + async/after_commit may be notified. Rejections and unsuccessful outcomes
are logged locally; no response is fabricated. Batch params/result are each one
object containing items, never a JSON-RPC array. They contain 1..64 unique
invocation IDs. Acknowledged batch requests accept observation actions in
sequential, parallel, async and after_commit modes, excluding filters and bail
gates. Batch notifications restrict every item to async/after_commit. The SDK
validates every item before starting any. Per-item results stay in input order;
operational failures do not erase other results. Dispatch invokes the handler for
each item; mode describes the host-selected catalog semantics, not a promise to
reschedule the host's dispatch in the SDK.

Receiver monotonic leases start before hook DTO decoding and batch queueing and
are bounded by timeout_ms, aggregate_budget_ms and parent cancellation. Absolute
UTC RFC3339 deadline (at most nine fractional digits) is diagnostic and cannot
extend the lease. A late handler result is discarded. Timers and elapsed-time
checks cover an event loop that is delayed by synchronous author code. The host
still owns the original monotonic deadline, admission/writer/transport charges,
ancestry, root budget, cycle/depth fences, breaker policy and unload revocation.
Cooperative cancellation cannot terminate arbitrary author goroutines or JS code.

Results have invocation_id and status. ok filter results require payload, including
explicit null; ok action results forbid it. cancelled/approval_required permit only
optional reason and are valid only for bail actions. failed/unavailable require
error containing a known failure code and optional message. Contradictory branches,
unknown statuses/codes or invocation mismatches are invalid output. TS author
results choose payload OR validated payloadJSON, never both. Host result decoding
returns an immutable snapshot and retains raw payload via hookPayloadJSON; encode functions reuse that snapshot.
Go host EncodeHook* functions avoid HTML escaping; hook Serve replies use the bounded frame encoders, normalize nonliteral framing whitespace, and preserve
payload literals. Use the named codecs when exact opaque representation matters;
a generic serializer may normalize it.

HookRPCError/hookRPCError map structural refusals to -32602 (invalid_params),
-32601 (profile_unavailable/method_not_found), -32600 (invalid_request) and -32700
(parse_error), with data={contract:"hooks/1",code,field?}. Malformed base envelopes
are rejected before method dispatch and keep base envelope errors. Serve's hook
DTO errors use the safe outer field params; public validators expose detailed
field diagnostics. -32003 is never a hook veto and -32010 remains host-rpc/1.
Returned/thrown handler errors, panics, invalid output and expired/cancelled leases
become per-invocation operational results, not deliberate vetoes. No engine
sentinels or plugin-hooks dependency enter this SDK; host adapters map structured
statuses to their own engine outcomes.

The normative hooks-handling, hooks-negotiated, hooks-declined, hooks-no-offer
and hooks-wrong-version transcripts run through both in-process and real Go/Node
child paths. Negotiated paths use the normal Init offer and handler, without
fixture injection. They cover acknowledgement and its absence, independent reverse
decline, strict version errors, all result branches, action modes, payload
literals, escaped keys, batch bounds/order and notification silence. hooks.json
adds raw DTO/presence/integer/branch vectors. The plugin-hooks bridge's hookstest
R16-R22 acceptance satisfied the hooks acknowledgement gate. Hosts still own
catalog policy and authority; reverse callbacks, latency measurement and releases
remain separate work.
