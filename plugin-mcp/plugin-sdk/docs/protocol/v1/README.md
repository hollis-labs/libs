# plugin-sdk stdio protocol v1 — draft

CW-20261002-0139. This is a language-neutral description of the existing Go
wire contract, with executable observations and explicitly pending proposals.
It is provisional until the JS/TS spikes (0141/0142); it does not freeze the
manifest or authorize a change to either runtime.

Primary sources read at SDK commit `8668e05`: `subprocess/protocol.go`,
`types.go`, `types_sdk.go`, `server.go`, `capability.go`, root `errors.go` and
`envelope.go`. Host behavior was checked against the authored `plugin-host`
`process.go`, `conn.go` and `client.go` checkout at
`725175778322e102074c52cd5af3b0391b9831ef`, read only. A type declaration
is not evidence that its method is implemented.

## Transport and conversation

The host writes UTF-8 JSON, one complete compact object per line, to the
worker's stdin; the worker writes one response object per line to stdout.
There is no Content-Length framing or JSON-RPC batch support. JSON whitespace
within a line is fine; a literal newline inside a string must be escaped.
Stdout is exclusively protocol traffic; logging goes to stderr as JSON lines.
Go's logger redacts registered secret values, but arbitrary plugin writes are
not automatically redacted.

A request has `jsonrpc: "2.0"`, a nonzero integer `id`, a method string, and
optional `params`. A notification omits `id` (the Go wire encoder also omits
zero); explicitly setting zero has the same behavior. IDs correlate replies,
not ordering. Go accepts signed int64; cross-language producers should use
positive JavaScript-safe integers (1 through 9007199254740991). Strings and
fractional IDs are not supported by this v1 profile.

A response has `jsonrpc: "2.0"`, integer `id`, and exactly one of `result` or
`error`. Errors contain integer `code`, string `message`, and optional opaque
`data` (the Go dispatcher never populates data). Parse failures use `id: 0`,
not null, and always produce a reply, even without a recoverable request ID.
Ordinary notifications never produce success or failure replies.

Go dispatches each decoded request concurrently and serializes complete
stdout writes. Replies can arrive out of request order. Hosts must await the
init reply before load, and load before invoking registered handlers; the
SDK itself has no lifecycle state machine. There is no wire request-cancel
method, concurrency limit, or per-request deadline in Go Serve. ErrCancelled
is a plugin veto result, not transport cancellation.

## Frame limits and shutdown

| Direction | Current boundary |
| --- | --- |
| Host to Go SDK | Scanner buffer ceiling 8 MiB (8388608 bytes). A newline-terminated frame with 8388607 payload bytes is accepted; a payload of 8388608 bytes is rejected. |
| Host outbound (`plugin-host`) | Encoded JSON plus newline at most 8 MiB by default; oversized requests fail locally without writing bytes. Configurable with WithMaxFrame. |
| Go SDK outbound | No response-size cap in Serve. |
| Host inbound (`plugin-host`) | Default response-line cap 64 MiB, configurable with WithMaxInboundFrame; oversize lines are drained/discarded and counted, leaving the connection up. |

The transcript format compactly describes large frames instead of storing
megabytes of padding. A scanner-limit failure ends Serve with an error after
final Unload; it does not emit a JSON-RPC frame. Malformed JSON within the
limit emits a parse error and reading continues. Request errors and panics do
not end the conversation.

Stdin EOF ends input, waits for in-flight handlers, invokes Unload, and returns.
SIGTERM/SIGINT cancel the shared handler context and initiate final Unload.
An explicit `plugin/unload` invokes Unload and acknowledges it but does not
stop Serve. A host's ordinary stop (unload RPC followed by closing stdin)
therefore invokes Unload again. Plugins must tolerate repeated cleanup. Signal
handling, OS process reaping, host drop counters and scheduling under load are
outside this corpus; existing runtime tests remain relevant.

## Handshake and registrations

1. Host calls `plugin/init` with paths `plugin_dir`, `data_dir`, `cache_dir`,
   resolved string map `config`, `log_level`, and `host_info` containing host
   version and protocol. `granted` is an optional string array; `identity` is
   optional opaque JSON. Unknown fields are ignored by the Go decoder.
2. Plugin replies with `id`, `name`, `version`, `description`, `protocol`.
   `plugin-host` checks protocol equals 1 exactly and ID is nonempty before
   continuing; a mismatch is a failed handshake, not range negotiation.
   Serve delegates Init to plugin code and does not check the incoming number.
3. Host calls `plugin/load` with absent params or `{}`. The result is `{}` or
   `{skipped_registrations: [{kind, id, reason}, ...]}`. Registrations belong in
   `plugin.yaml`; load does not return a runtime registration catalog.
4. Where applicable, the host invokes `plugin/migrate` before load. Its params
   are `from_version`, `to_version`, `data_dir`. Go forwards only from/to to
   the optional Migrator and returns `{}` on success.

Granted names have host-defined meaning. The SDK carries them without granting,
checking or enforcing permissions. An absent entry does not distinguish an old
host from a host that granted nothing. Identity is likewise carried without
verification. IdentityAware, when implemented, is called after successful Init
and before command/event/MCP/HTTP handlers when identity JSON is present. CRUD
and migration do not carry per-call identity in this wire contract.

## Methods and wire payloads

The complete field shapes are in `protocol/v1/schema.json`; `wire.go` and
`wire.ts` are generated reference views of that schema. The existing Go
`subprocess` author API remains unchanged. The schema describes complete wire
producer shapes, not all lenient decoder inputs: Go accepts omitted/null
params, fills absent fields with zero values, and ignores unknown fields.
It is not installed as a new runtime validator. Opaque JSON fields remain
opaque; binary HTTP bodies are standard base64 strings, not arrays or UTF-8.

| Method | Params → result | Optional handler |
| --- | --- | --- |
| plugin/init | InitParams → InitResult | Required |
| plugin/load | EmptyParams → LoadResult | Required |
| plugin/unload | absent → `{ok:true}` | Required |
| plugin/health | absent → `{ok, message?}` | HealthChecker |
| command/execute | `{name, session_id, args, identity?}` → `{action, content?, envelopes?}` | CommandHandler |
| event/handle | `{type, source, data, pre_hook, session_id?, identity?}` → `{cancel?, reason?, envelopes?}` | EventHandler |
| crud/create | `{resource_type, data?}` → `{data}` | CRUDHandler |
| crud/read | `{resource_type, id?}` → `{data}` | CRUDHandler |
| crud/update | `{resource_type, id?, data?}` → `{data}` | CRUDHandler |
| crud/delete | `{resource_type, id?}` → `{ok:true}` | CRUDHandler |
| crud/list | `{resource_type, filters?}` → `{items:[...]}` | CRUDHandler |
| mcp/call_tool | `{tool_name, arguments, session_id?, identity?}` → `{content, is_error?, envelopes?}` | MCPHandler |
| http/handle | HTTPRequest → `{status, headers?, body?}` | HTTPHandler |
| plugin/migrate | MigrateParams → `{}` | Migrator |

An envelope is `{type, data, session_id?}`. Names and payloads are
host/manifest-defined. HTTPRequest also carries `method`, `path`, optional
`raw_path`, `raw_query`, flattened `query` and `headers`, `body`, `session_id`,
and `identity`. Raw query preserves repeated/empty values and escaped path
separators; the flattened maps alone cannot. HTTP is buffered, not streaming.
MCP `is_error` is a tool-level failure inside a successful RPC result.

Missing optional handlers return -32601, except health defaults to
`{ok:true}` and event notifications are silently dropped. In particular,
missing Migrator is an error despite older type comments suggesting a no-op.
Empty CRUD lists encode as `[]`, not null. False/empty optional result fields
are omitted by Go. `cancel:true` pre-hook results and ErrCancelled errors
represent distinct wire outcomes.

## Errors

| Condition | Code | Message behavior |
| --- | --- | --- |
| Malformed JSON or Go request decoding failure | -32700 | `parse error:` plus decoder detail |
| Unsupported method / missing handler | -32601 | Unknown method or missing interface name |
| Params cannot decode to a supported handler's type | -32602 | `decode params:` plus decoder detail |
| Internal error or recovered handler panic | -32603 | Error message, or `panic: ...` |
| Typed plugin error with HTTP status 404 | -32000 | Typed error's own message |
| Typed plugin error with HTTP status 409 | -32001 | Typed error's own message |
| Typed plugin error with HTTP status 422 | -32002 | Typed error's own message |
| ErrCancelled (including wrapped sentinel) | -32003 | Entire returned error string |

Typed errors are unwrapped and their underlying message is used. Cancellation
is tested before typed errors. Other typed statuses fall back to -32603.
The mapping applies to command/event/CRUD/MCP/HTTP/migration, irrespective of
whether an event is marked pre-hook. Init/Load/Unload errors always use -32603;
health errors return a successful `{ok:false,message}` result. Notifications
suppress every ordinary reply, including errors and panics.

## Findings and pending proposals

These are observations to reconsider after the spikes, not Go changes in this
task. `decoder-findings.json` records permissive Go behavior, rather than
requiring a future strict implementation to conceal that divergence.

- **F1 — no host/* reverse calls.** Go Serve reads requests only, never sends
  requests; plugin-host Conn drops inbound frames containing a method. Config,
  filesystem helpers, and stderr logging are local helpers, not host RPC.
  Proposed, not v1-normative: plugin-to-host requests use `{jsonrpc:"2.0",id,
  method:"host/storage/get",params:{key}}` with `{value:<JSON>}` results.
  The host must own per-call grant checks. `host/secrets/get`,
  `host/egress/request`, `host/events/publish`, and `host/log` are candidate
  method names, not an SDK capability vocabulary. Duplex routing, ID ownership,
  denial errors, resource scoping, cancellation and budgets need design and
  a host implementation before this can be promised. The pending transcript
  illustrates direction and correlation only; it proves no enforcement.
- **F2 — mcp/list_tools is a constant, not a method implementation.** Go returns
  -32601 and plugin-host deliberately has no typed client method. Tools are
  declarative. Proposed, not v1-normative: `{tools:[]}` as an empty catalog;
  pending fixture is an opt-in proposal, not a migration requirement.
- **F3 — JSON-RPC validation is partial.** Go does not check the jsonrpc field,
  method presence or handshake state. String IDs and batches become -32700;
  missing/wrong version fields can still dispatch. -32600 is declared but
  never emitted by this dispatcher. Parse replies use zero rather than null.
- **F4 — lifecycle and migration asymmetries.** Incoming protocol validation is
  the plugin's/host's job, unload repeats on EOF, lifecycle typed errors are
  internal errors, and a missing Migrator yields method-not-found. The prose
  comments on MigrateResult do not override actual dispatch.
- **F5 — asymmetric caps.** SDK input has a scanner ceiling and SDK output is
  unbounded; host caps are configurable policies. Do not assert one symmetric
  8 MiB rule for every direction.
- **F6 — Go IDs exceed JS number precision.** The Go wire type is int64.
  Generated TS uses number; callers must remain in the safe integer profile
  or a later runtime must explicitly preserve larger integer tokens.

## Corpus and regeneration

Run `go test ./subprocess -run TestProtocolTranscripts`. Run the full repo gate
through `heavytest` as described in AGENTS.md/MISSION.md. Regenerate views with
`python3 protocol/v1/generate.py` (Python stdlib plus gofmt). There is no
file-sync assertion; regenerate and review generated changes when editing the
schema. Shared schema types are reference wire DTOs, not a replacement for
plugin interfaces, helpers, or TS server implementation.

Each JSON transcript starts a fresh fixture plugin with `profile`, optional
`status` (default observed; proposed means pending), `finding`, `steps`, and
optional `termination`. A step supplies `send` (a JSON value compacted to one
line) or `raw` (literal line content). `repeat` repeats raw content; `pad_bytes`
appends ASCII spaces until the payload has exactly that many bytes. The runner
appends one LF. These expansion fields apply before sending, not to replies.

When `expect` is present, await that reply before the next step. Otherwise the
step is a notification and gets no reply. Compare JSON structurally, ignoring
object key order but preserving arrays, nulls and omissions. If
`message_prefix` is present, require that prefix on error.message and compare
all remaining fields to expect; only language-specific decoder wording is
relaxed. After all steps, close stdin, drain stdout to EOF, reject extra replies,
and await Serve termination. `termination: "frame-too-large"` requires a terminal frame-limit error; each
runner maps its runtime diagnostic to this semantic outcome (the Go diagnostic
is "stdin scanner: bufio.Scanner: token too long"). Timeouts bound the replay, not protocol behavior.
A TS runner should report divergences explicitly when its parser diagnostics or
strictness differ. Proposed fixtures are skipped by default; a future opt-in
adapter must implement their profiles and honor direction.

Fixture plugin recipe (the Go implementation is `subprocess/conformance_test.go`):

- **base:** Init always returns fixture/Fixture/1.0.0/conformance/protocol 1;
  Load declines command optional with reason "no config"; Unload succeeds;
  no optional handlers.
- **full:** base lifecycle plus every optional runtime handler. Command echoes
  args and emits fixture.echo with data `{name,identity}` (missing identity is
  null) and the incoming session. Event returns `{cancel:true,reason:"veto"}`
  for pre_hook, otherwise `{}`. CRUD create/update echo data, read returns
  `{id,resource_type}`, delete succeeds, list is empty. MCP echoes arguments as
  content and marks only tool-error as is_error. HTTP echoes binary body with
  status 201 and content-type application/octet-stream. Health is OK; migrate
  succeeds. Identity is directly carried; no IdentityAware callback in this
  fixture profile.
- Error controls shared across full handlers: command name, event type, CRUD
  read ID, MCP tool_name, HTTP path or migration from_version selects:
  not-found → wrapped typed 404 "missing"; conflict → 409 "exists";
  validation → 422 "invalid"; cancelled → wrapped ErrCancelled;
  internal → "failed"; other-status → typed 403 "denied"; panic →
  panic/throw "fixture panic". CRUD create/update/delete/list do not use these
  controls. The cancellation wrapper message is
  "wrapped: plugin: action cancelled by hook".
- **lifecycle-error:** base with Init returning typed 404 "missing" and Load
  returning the unwrapped cancellation sentinel.
- **health-error:** base with health returning error "unhealthy".
- **duplex:** reserved proposed adapter, not implemented in Go.

This corpus is shared behavioral evidence for Go and TS, not proof of every
host policy, identity callback, filesystem helper, logger, race, or sandbox
property. subprocesstest's existing tests run in the final Go gate; the new
wire replay intentionally uses Serve's real decoder rather than that friendly
harness, which would hide unsupported frames and serialization behavior.
