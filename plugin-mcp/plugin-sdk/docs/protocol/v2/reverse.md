# Reverse RPC negotiation

This activation candidate is held for merge until normal negotiated Go/Node
child replay and pinned plugin-host interoperability pass. Internal
fixture activation does not satisfy either requirement. The current child
driver's engine cases still select their private profile explicitly.

Authors opt in with Go `ServeOptions.ReverseRPC: true` or TypeScript
`serve(plugin, {reverseRPC: true})`. Both default to false. The host must supply
a valid `host_services` offer with `reverse_rpc_version: 1`, matching incarnation,
known unique methods, and a positive `method_timeout_ms` entry for every offered
method, without extra entries. Wrong versions and malformed offers retain the
strict typed Init rejection even when the author declines the profile.

The runtime acknowledges `reverse_rpc_version: 1` only after successful author
Init, result and identity handling, and selection of the successful Init reply
for publication. An authored acknowledgement, package version or DTO presence
cannot enable the profile. No offer or no opt-in completes base Init without
acknowledgement or a host client. A host requiring reverse must refuse that
decline before activating the plugin. Await the Init reply before ordinary work.

The accepted method set is exactly the offered implemented subset; an empty
valid set acknowledges transport without adding services. The SDK copies the
offer and grants before author code can mutate Init input. Methods not offered
remain unavailable; no method timeout is invented.

## Accepted ceilings

Every implementation limit is the minimum of the offered ceiling and the
configured local ceiling. Large offers do not widen local defaults. Before Init,
base/declined connections use local policy.

| Offer | Local implementation boundary |
| --- | --- |
| `host_to_plugin_inflight` | Ordinary forward handler slots, at most 16 |
| `plugin_to_host_inflight` | Pending reverse slots, at most 8 |
| `control_slots` | Lifecycle handler slots, at most 2 |
| `max_frame_bytes` | Both directions, at most 8 MiB including LF/optional CR |
| `max_queued_write_bytes` | **Each** writer lane, at most 8 MiB |
| `write_timeout_ms` | Whole physical write, at most the configured/default 5s |
| `method_timeout_ms` | Required offered uint32 ceiling for each offered method |

Queue frame counts stay local, at most 32 per lane. Ordinary and reply/control
lanes are independent: total queued bytes can reach twice the accepted per-lane
ceiling, plus one in-progress bounded frame. Terminal reservations remain
accounted during narrowing. Offers unable to carry their acknowledgement or
reserved terminal work fail closed; the SDK never widens them to fit a reply.
Whole shutdown time remains independently bounded. `host_global_inflight` and
`max_depth` remain host policy; there is no SDK host-global ledger or inferred
trusted depth. Forward calls without context have no invented SDK deadline.

Reader limits are sampled per frame, and a completed pipelined frame is checked
again against accepted policy before admission. Writer selection preserves
bounded encoding and whole-write receipts. Narrowing never replaces a partial
write. Directional activation seeds the incoming high-water with the Init ID;
later requests require increasing positive safe integer IDs. Opposite directions
have independent ID spaces. Init logging can demultiplex replies provisionally
without enabling ordinary callbacks while Init is still running.

## Client lifetime and lifecycle

Active ordinary callbacks with a live positive request ID and host-issued
`context.binding_id` receive Go `HostClientFromContext(ctx)` / TypeScript
`ctx.host`. A missing binding leaves the client absent. The forward callback may
still execute. The SDK builds reverse parent/binding metadata from the local
request scope, and each helper explicitly selects `grant_id`. Host authority,
live grant membership, generation, lease and admission/commit policy remain
host-owned. Cached clients cannot outlive their request or connection.

Only offered `host/log` with an explicit `log.write` grant is available during
Init, Load and bounded explicit-Unload cleanup. The host must deliver a live
lifecycle binding and the parent is the actual positive ID of the in-flight
Init/Load/Unload request. No completed Init ID or parent zero is borrowed.
Business helpers remain unavailable in lifecycle scopes. Provisional Init log
replies continue through the reader while Init awaits them; failure, timeout,
panic or invalid result revokes pending provisional work and cached clients.
Cancelling an in-progress write retains the existing transport fence rule.

Explicit Unload fences ordinary work, drains/cancels admitted callbacks, permits
bounded log under its own live scope and remaining shutdown budget, publishes
its one terminal reply, then exits. EOF/SIGTERM/disconnect have no host-issued
cleanup parent: cleanup receives no host client and can use redacted local
stderr logging. No detached/background binding is invented.

Hooks negotiate independently. Hook handlers have no host client, including
connections that acknowledge both profiles. Nested hook calls remain unavailable
with owner **SDK hook-context and plugin-hooks adapter maintainers**, pending a separate approved per-item
scope/client design. This is named unavailable coverage, never a conformance
waiver. The plugin-side `mcp/list_tools` proposal also remains unimplemented;
the adopted reverse `host/mcp/list_tools` is a different method.

`protocol/v2/fixtures/negotiation.json` exercises normal Serve opt-in and offers
in Go and TS, alongside scoped lifecycle, snapshot, frame, revocation and ID
tests. The merge evidence must additionally identify the negotiated child
corpus/head and real plugin-host source/runtime pins, its Conn/spawn/cancel/
dispose paths and typed backends. Fake SDK hosts cannot certify host authority,
security adapters, durable receipts, host-global limits or commit enforcement.
