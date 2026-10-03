# Protocol 2 stdio contract

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
normative/observed-quirk levels for payloads, framing and shutdown; those policies
have separate conformance work. The unchanged [v1 corpus](../v1/README.md) records
historical protocol-1 behavior and is no longer replayed against current Serve.
Optional profiles require their own conformance gate.
