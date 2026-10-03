# @hollis-labs/plugin-sdk

A host-neutral, zero-runtime-dependency server SDK for Node 22+ and Deno's
Node compatibility layer. Workers speak plugin-sdk stdio protocol 2. This
package is separate from the browser's `@hollis-labs/plugin-registry`.

```ts
import { serve, PROTOCOL_VERSION, type ServerPlugin } from '@hollis-labs/plugin-sdk';

const plugin: ServerPlugin = {
  init(ctx, params) {
    const token = ctx.config.secret('api_key'); // registers redaction
    ctx.logger.info('initialized', { hasToken: Boolean(token) });
    return { id: 'hello', name: 'Hello', version: '0.1.0',
      description: 'Example', protocol: PROTOCOL_VERSION, capability_contract: 1 };
  },
  load() { return {}; },
  unload() {},
  command(ctx, request) {
    return { action: 'message', content: `hello ${request.args}` };
  },
};
await serve(plugin);
```

Compile to ESM JavaScript before running with Node. Deno can run the same built
worker (`deno run --allow-env worker.js` when your worker needs env access).
TypeScript author projects need `@types/node` as a development dependency.
The SDK itself does not download npm modules or confer permissions at startup.
Install a bundled worker artifact as required by your host. This repository
prepares the package; publishing remains a separate owner action.

## Author API

`Plugin` requires `init(ctx, InitParams)`, `load(ctx)`, and `unload(ctx)`. Methods
can return directly or return promises. Capability interfaces extend it through
`ServerPlugin`: `command`, `eventHandle`, `mcpCallTool`, `httpHandle`, `health`,
`migrate`, and CRUD `create/read/update/delete/list` (all five are required to
advertise CRUD). `identity(ctx, value)` optionally receives opaque identities
after successful init and before command/event/MCP/HTTP dispatch. Identity is
carried without verification; missing identity does not call this method, while
explicit null does. Callbacks share the runtime's AbortSignal and receive its
config reader and logger.

Request/result field names follow the snake_case wire types. HTTP author bodies
are `Uint8Array`; the runtime converts them to/from base64 JSON strings. MCP
content and identity are opaque JSON. Registrations come from the host's
manifest; load may return only `skipped_registrations`. Migration receives
from/to versions. Capability and envelope names belong to the host.

Throw `errNotFound(message)`, `errConflict(message)`, `errValidation(message)`,
or `PluginError(status, message)` for typed errors; throw `ErrCancelled` for a
veto. Native `Error.cause` wrapping is supported. Cancellation takes precedence
and preserves the outer error message; typed errors use their underlying
message. Ordinary thrown/rejected errors map to -32603 and the loop survives.
Init/load/unload/health callback failures use that same typed mapping. An authored
`{ok:false,message}` health status is successful; a callback failure is an RPC error. A successful event veto is
`{cancel:true,reason}` and is distinct from throwing cancellation.

## Transport and lifecycle

`serve(plugin, {input, output, stderr, signal, handleSignals, shutdownTimeoutMs})` accepts injectable
Node streams for testing. Defaults use process stdout/stderr and Node process stdin (Deno native stdin
through a stream adapter under Deno), and handle
SIGINT/SIGTERM; injected input defaults to no process signal listeners. In-flight
handlers are concurrent, so callers must await init, then load, before invoking
registered capabilities. Replies queue as complete JSON lines and preserve
correlation IDs; completion order can differ from request order. Write callbacks
provide stream backpressure. Output errors reject serve after draining and
cleanup. A throwing or rejecting handler does not reject serve itself.

The input ceiling is 8 MiB: up to 8388607 payload bytes plus LF, matching Go's
scanner. Counting uses UTF-8 bytes across chunks; CRLF and a final frame without
LF are accepted. Oversize input rejects serve with `FrameTooLargeError`, invokes
final unload, and emits no RPC error. There is no output frame cap or in-flight
request quota, matching the current Go contract.

Explicit unload is terminal: it fences new work, aborts and drains admitted
handlers, attempts cleanup once with a fresh signal, flushes its result/error,
and ends Serve without waiting for host EOF. EOF and signals share that path;
cleanup failures and throws are never retried. The total drain/cleanup/flush
budget defaults to five seconds (`shutdownTimeoutMs`, positive finite value up to 2147483647 ms).
`ShutdownTimeoutError` means shutdown is incomplete; callback code cannot be
forcibly killed in-process and must yield for the deadline to run. A drain
timeout does not invoke cleanup concurrently with an uncooperative handler.
Injected input/output/stderr remain caller-owned. Serve detaches input listeners
and does not destroy those streams; callers close outstanding I/O on transport
failure. See the [protocol-2 lifecycle contract](../../../docs/protocol/v2/README.md#lifecycle-shutdown-and-errors).

## Config and logging

`ctx.config` exposes `string`, `bool`, `int`, `secret`, `required`, `has` over
host-resolved config. `required` throws on missing/empty, not whitespace.
`bool` follows Go's true spellings; invalid values return false. `int` accepts
signed decimal JavaScript-safe integers, otherwise zero. `secret` records a
nonempty value in the instance's shared SecretTracker. `hasCapability(params,
name)` checks grant-name membership; a match is discovery data and still needs live host authorization. `resolvedDataDir`
throws when the host supplies no persistent data directory.

`ctx.logger` writes JSON lines to stderr with ts/level/msg. `with(fields)`
creates a child sharing the same tracker. Registered secrets are replaced in
message text and all JSON string values, including nested fields; logging
unserializable data uses a redacted fallback. Field keys and arbitrary direct
writes/console logging are outside this guarantee. Config/logger state is not
global and cannot accidentally cross independent serve/harness instances.
This redaction is stronger than Go's exact-match top-level field redaction.

## In-process harness

```ts
import { createHarness } from '@hollis-labs/plugin-sdk/test';
const h = await createHarness(plugin, {
  config: { api_key: 'fixture' }, grants: [],
  jsonRoundtrip: true,
});
try {
  await h.init();
  await h.load();
  const result = await h.command('hello', 'session', 'world');
  await h.unload();
} finally { await h.close(); }
```

The harness calls author methods directly and preserves thrown errors. It
provides init/load/unload, command/event/health, CRUD, mcp/http/migrate helpers.
JSON mode clones request/result data and rejects cycles, BigInt, functions and
symbols; HTTP bytes use the real base64 wire representation. Without JSON mode
references are preserved. `PLUGIN_SDK_JSON_ROUNDTRIP` enables JSON mode unless
explicitly overridden (empty/0/false disable it). The harness creates plugin,
data and cache directories under the OS temporary root, or uses a supplied
pluginDir without owning it. `close()` removes only its own tree and is
idempotent; unload is explicit. Logs default to a discard sink; `writeLog`
can capture them for assertions. Direct harness calls do not simulate the
IdentityAware dispatch callback or RPC error mapping; the real Serve corpus
covers the wire layer.

## Shared contract and known differences

Wire types are generated from `protocol/v2/schema.json` by
`python3 protocol/v2/generate.py`; do not edit src/wire.ts. The shared corpus
at `docs/protocol/v2/transcripts` is replayed by test/transcripts.test.js. Every
observed fixture must pass; proposed reverse `host/*` and `mcp/list_tools`
fixtures stay skipped. The SDK implements neither proposal.

Known differences outside the observed corpus, recorded for post-spike review:

- Envelope IDs are strings or safe integers in both SDKs; fraction/exponent
  tokens and duplicate top-level keys are invalid. The shared protocol-2 corpus
  covers that contract. Config integers outside the JS-safe range return zero
  rather than Go's full platform integer range.
- Non-init payload fields still inherit historical defaults: known fields are
  matched case-sensitively in TS, while Go's struct decoder also matches
  case-insensitively; duplicate payload keys can use the last value. These
  method-owned policies have a separate validation stage.
- Decoder message detail is runtime-specific. JavaScript has no independent
  distinction between returned errors and Go panics; thrown Error maps as an
  error, while a non-Error thrown value gets a `panic:` prefix. Both SDKs map
  failing health callbacks to RPC errors.
- JSON values can differ at numbers outside JS precision and at unsupported
  values such as undefined. Use JSON-safe payloads. The optional harness catches
  common impossible values; it is not a schema validator.
- Logger redaction covers nested string values/message text in addition to
  Go's top-level exact string matches. Host enforcement/sandboxing, filesystem
  data/cache helpers, output filtering and browser UI loading are not provided
  by this package.

Validation runs Node's test runner, the same tests under Deno's Node test
compatibility, and real stdio worker acceptance on Node 22/24 and Deno. The
stdio acceptance includes EOF, handler isolation, redacted stderr and SIGTERM.
EOF concurrency and signal cooperation have separate behavior tests because the
shared transcripts deliberately serialize request/reply steps.

## Author build helpers

`@hollis-labs/plugin-sdk/build` is a separate Node-only, zero-dependency author
build surface for manifest-v2 artifact collection, canonical writing and bundle
verification. It never enters the plugin worker/browser runtime and does not
implement worker transport profiles. See
[the TypeScript authoring guide](../../../docs/ts-plugin-authoring.md) for staging,
schema generation and host reload boundaries.
Protocol 2 requires the strict Init contract described in
[the handshake](../../../docs/protocol/v2/README.md). Harness options `grants`
and `incarnation` replace the old name-only grant option; default grants are
empty and the harness supplies a test incarnation. Results acknowledge both
`protocol:2` and `capability_contract:1`. Optional reverse/hook offers are
validated and declined by the current runtime; their transports are not implemented.

Runtime params require the fields in the protocol-2 [payload matrix](../../../docs/protocol/v2/payloads.md). Optional `context` reuses the closed `ForwardContext` DTO; callbacks read `context.forwardContext`. It carries metadata only; this runtime does not authorize bindings or enforce its timeout. Invalid params return -32602 before invocation, and unrepresentable or malformed results return -32603.
