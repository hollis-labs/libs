# Protocol 2 stdio contract

See the [reverse-RPC field contract](host-rpc.md) for the approved optional
profile schema and unexecuted fixtures. Transport implementation and profile
acknowledgement remain separately gated.

Go `subprocess.ProtocolVersion` and TS `PROTOCOL_VERSION` are 2. Host and plugin
use one strict Init exchange before load or ordinary handlers. The host builds,
validates and encodes the complete payload before spawning the plugin. There is
no protocol-1 fallback or second Init type.

```json
{"jsonrpc":"2.0","id":1,"method":"plugin/init","params":{"plugin_dir":"/plugins/example","data_dir":"/data/example","cache_dir":"/cache/example","config":{},"log_level":"info","host_info":{"version":"1.0.0","protocol":2},"capability_contract":1,"incarnation":{"host_instance":"host-epoch","owner_id":"example.plugin","owner_generation":1},"grants":[]}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"id":"example.plugin","name":"Example","version":"1.0.0","description":"","protocol":2,"capability_contract":1}}
```

Every shown field is required and non-null. Directories and host version are
nonblank strings; config is a string map (empty `{}` is valid); log level is
`debug`, `info`, `warn` or `error`. Result ID/name/version must be nonblank;
description may be empty. `incarnation` is the host-issued tuple, unrelated to
manifest `server.runtime` (the execution engine). Every grant copies the tuple.
Empty grants are `[]`; omission, null, malformed objects and foreign tuples fail.
See [grants](grants.md) and [the schema](../../../protocol/v2/init.schema.json).

DTO objects are closed, including nested grants and profiles. Unknown fields,
wrong casing, duplicate decoded keys and unpaired Unicode surrogates are
rejected before ordinary JSON parsing. Opaque `scope` and optional `identity`
retain their own key casing. Identity is a courier, not authentication; Init
identity is never substituted for a later per-call caller. Present null identity
is invalid. Integer security fields require decimal integer tokens, not exponents
or fractions. Schema alone cannot enforce duplicate-key or raw-token constraints;
use SDK decoders/validators. JSON nesting is bounded to 128 levels.

The host advertises 2; the plugin validates before invoking its Init callback
and acknowledges 2 plus capability contract 1. Go `InitError` and TS `InitError`
expose `invalid_init`, `protocol_mismatch`, `capability_contract_mismatch` and
`profile_mismatch`. Invalid/mismatched payloads return -32602 with data contract
`plugin-init/2`, code, field and expected/received for version mismatch. Diagnostics
do not echo rejected values. Lifecycle ordering fails with -32600: one Init
attempt per connection, positive safe-integer request ID, successful Init required
before handlers. Init is a barrier for pipelined requests. Host validates the
result and expected plugin identity/version before loading and activating.

Optional `host_services` offers reverse profile version 1, a matching incarnation,
closed shared method inventory and finite uint32 limits. Optional `hooks_profile`
offers `{ "hooks_profile_version": 1 }` independently. Result acknowledgements
are `reverse_rpc_version:1` and `hooks_profile_version:1`; an acknowledgement
without its offer fails. Current Serve implementations validate offers and omit
both acknowledgements, visibly declining them. Hosts requiring either profile
fail before activation. No reverse RPC, hook dispatch or HTTP fallback is supplied.
Application host errors -32010 and contract `host-rpc/1` belong to the later
reverse profile, not Init errors.

## JSON-RPC envelopes and IDs

Each frame contains one JSON object with `jsonrpc` exactly `"2.0"`. Requests
require a string `method`, never `result` or `error`. IDs are strings (including
empty strings) or integers within ±9007199254740991. Numeric tokens must use
integer form: fractions and exponents are invalid even when their value is
integral. Zero and negative IDs are ordinary request IDs. Init additionally
requires a positive safe integer ID. Only an absent ID denotes a notification;
explicit null is invalid on a request. Notifications run without success/error
replies. Method payload rules remain separate from envelope validation.

Malformed JSON produces -32700 with `id:null`. Valid JSON with an invalid
envelope, including any array/batch, produces -32600. A unique valid ID is echoed
on structural errors; missing, invalid or duplicate IDs produce `id:null`.
Duplicate top-level decoded keys (including escaped spellings) are invalid.
String IDs and methods require valid Unicode, including paired surrogates.
Responses require an ID and exactly one of `result` or an error object with an
integer `code` and string `message`; null IDs are allowed only on error replies.
Request/reply mixtures are invalid. Structurally valid unsolicited replies are
dropped without dispatch or response: this runtime has no outgoing waiters yet.

Go `subprocess.RPCID` is a comparable tagged value. Use `NumberID(n)` or
`StringID(s)` in `RPCRequest` and `RPCResponse`, and `Integer()` / `Text()` to
inspect it. Its zero value omits the request ID and encodes a null response ID;
`NumberID(0)` and `StringID("")` remain present. This replaces the previous
`int64` field with a source API break. TS uses `RPCID = string | number` and
omits `id` for notifications; there is no bigint or rounded numeric ID support.

`transcripts/decoder-findings.json`, `notifications.json` and `envelope-ids.json`
assert normative envelopes in Go and TS. Other transcripts retain their existing
normative/observed-quirk levels for framing; runtime payload validation is
normative as described in the payload matrix. The unchanged [v1 corpus](../v1/README.md) records
historical protocol-1 behavior and is no longer replayed against current Serve.
Optional profiles require their own conformance gate.

## Lifecycle shutdown and errors

A valid `plugin/unload` is terminal. Without successful Init it receives -32600
while still ending the connection through final cleanup. The reader fences new
work, cancels admitted handler contexts, drains admitted callbacks, and attempts
Unload once. The result `{ "ok": true }` or mapped callback error is the sole
correlated terminal reply, flushed before Serve returns; no host EOF is needed.
A notification unload terminates without a reply. Frames after the unload fence
are not dispatched or answered. EOF, SIGINT/SIGTERM, external cancellation and
transport failure use the same cleanup path. An unload error or panic is recorded
and never retried automatically. Cleanup uses a fresh context/signal, limited by
the remaining shutdown budget, rather than the cancelled handler context.

Init, Load, Unload and Health callback errors use the ordinary plugin error
mapping, including wrapped typed errors and the hook-veto sentinel. Init's
structural failures retain `plugin-init/2`. A missing Health callback defaults
to `{ "ok": true }`; an authored unhealthy status remains a successful
`{ "ok": false, "message": "..." }`. A callback failure or panic produces an RPC
error. Cancellation of a runtime context is separate from the plugin's hook veto.
Explicit unload callback failure is reported on the wire; cleanup failure without
an explicit unload reply is returned/rejected by Serve.

The default total shutdown budget is five seconds for drain, cleanup and output
flush together. Go exposes `ServeWithOptions(plugin, ServeOptions{Context: ctx,
ShutdownTimeout: duration, Input: reader, Output: writer})`; omitted streams use
stdin/stdout, and zero timeout uses `DefaultShutdownTimeout`. TS accepts
`shutdownTimeoutMs` (default `DEFAULT_SHUTDOWN_TIMEOUT_MS`, 5000) with its existing
`serve` options (positive finite value, at most 2147483647 ms). Budget exhaustion returns Go `ErrShutdownTimeout` or rejects
with TS `ShutdownTimeoutError`, reporting incomplete cleanup/transport. There is
no success acknowledgement for unfinished cleanup. If handlers do not drain,
Unload is not invoked concurrently with them or scheduled later as a retry.
Callback code ignoring cancellation may still be running after Serve returns;
these in-process runtimes cannot kill it. Late callbacks cannot enqueue replies.
TS requires callback code to yield to the event loop for its deadline to run.

Injected I/O remains caller-owned. TS detaches its input listeners at shutdown
and does not destroy injected streams. Go cannot interrupt an arbitrary injected
Reader/Writer: the caller must close or otherwise unblock outstanding I/O after
Serve returns, and must not reuse that I/O while an old operation is blocked.
An already-started injected write cannot be retracted; close the transport on
failure. Only runtime-owned pipes may be closed to wake blocked operations.

`lifecycle.json`, `lifecycle-errors.json`, `health-error.json` and
`lifecycle-shutdown.json` are normative. The shared shutdown recipe checks one
observed Unload attempt and zero post-fence Health callbacks. Runtime tests cover
cancellation/drain barriers, cleanup throw/panic, EOF/unload races and deadline
exhaustion. Reverse profiles retain separate implementation gates.

Runtime method params and results follow the [required/default matrix](payloads.md).
Every forward params DTO permits optional closed ForwardContext metadata.
`payload-validation.json` is normative, including scanner-based escaped-key
preservation and invalid-unload recovery. Both directions use the bounded framing policy below.

Both stdio directions default to an 8 MiB frame limit, including the terminating
LF. Input accepts one optional CR immediately before LF and counts it against
the limit; output emits LF only. Frames must contain valid UTF-8. EOF with bytes
remaining before LF is a truncated transport, never an implicit final frame.
Oversized input fails the connection without draining the rest of the line.

Go `ServeOptions.FrameLimits` (`InputBytes`, `OutputBytes`) and TypeScript
`ServeOptions.inputFrameBytes` / `outputFrameBytes` may narrow these ceilings
before Init. Go zero values use the defaults; TS omitted options use defaults.
These local options do not activate reverse RPC or hooks profiles.
`FrameTooLargeError` exposes only direction and limit, never frame contents.

Output JSON is staged within the configured byte budget before publication;
string escaping and base64 expansion count. An oversized result produces a
bounded correlated internal error when that error fits. If even the error does
not fit, the connection is fenced without emitting the rejected frame. A write
failure or partial write fences the connection and never appends a replacement
response to potentially partial JSON. Go `WriteTimeout` and TS `writeTimeoutMs`
bound a single transport write (default five seconds), independently of EOF and
the shutdown budget. Injected streams remain caller-owned after timeout: the
caller must release any already-started operation before reusing the stream.
The SDK cannot bound allocation or blocking inside plugin-authored serializers,
getters or callback code.

`frame-accepted.json`, `frame-limit.json`, `frame-output.json` and
`framing-crlf.json` are normative shared runtime transcripts. Focused runtime
tests cover malformed UTF-8, truncated EOF, blocked writes, partial writes,
base64 expansion and an error that cannot fit the output budget.

[Reserved hooks/1 wire and host codecs](hooks.md) describe the independently gated hook methods and shared hook conformance fixtures.

## Reader and correlation foundation

Hosts must await the Init reply before sending other ordinary requests. Requests
pipelined while Init runs are refused with -32600 (successful init required),
without invoking their callbacks. A live duplicate request ID fences the base
connection without a second callback or reply. An ID remains live until its
terminal reply has completely written; later base reuse is permitted, and string
IDs and safe integer IDs (including zero) retain their identity.

The internal duplex engine has separate incoming and outgoing ID tables and
registers pending calls before publication. It uses the bounded frame encoder and
one physical writer with whole-write receipts. During terminal unload it keeps
reading pending replies while draining and running once-only cleanup. EOF,
write failure and final closure complete pending calls once. Directional state
fences malformed reply candidates; base syntax/structure faults retain the
-32700/-32600 recovery contract. Valid unknown/late/duplicate replies cause no
response loop.

Shared `protocol/v2/fixtures/duplex-correlation.json` and `duplex-invalid.json`
run in Go and TS. Their normative base cases cover duplicate IDs and ID reuse;
`internal-core` cases exercise explicit fixture directional state, including
opposite-direction id=1, immediate/out-of-order replies and invalid correlated
results/errors. Runtime fixtures separately prove replies during pending Init
and cleanup. These are engine evidence, not negotiated reverse support or child
interoperability evidence. Production still declines reverse acknowledgement;
there is no public arbitrary-method host caller. Admission scheduling,
cancellation/deadline policy, author helpers and negotiated activation belong to
later slices.
