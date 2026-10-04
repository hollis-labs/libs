# Request-scoped host service clients

Go `subprocess.HostClientFromContext(ctx)` and TypeScript `ctx.host` expose an
SDK-owned client. Its core seven helpers cover storage get/put/delete, secrets get, egress
request, events publish, and log. Authors consume this client; they never
implement it. Later methods extend the same type.

Activation is currently private to conformance fixtures. Base and hooks-only
production connections have no host client. Implementing a hook handler does
not enable reverse RPC or give it a client. Negotiated reverse activation is a
separate slice. There is no public constructor, enable switch, or generic call.

Each call explicitly selects `grant_id` and supplies business arguments.
The SDK copies the validated Init grant snapshot and offered method ceilings;
it builds the required ReverseContext from the active request's host-owned
parent ID and binding reference. A cached client cannot acquire another
request's authority or start work after its owner terminates. Lifecycle scopes
permit only log, and hook handlers receive no client.

The call budget includes validation and serialization, and narrows to the
selected grant's `expires_at`, request deadline, offered method ceiling, and
caller budget. Go accepts a derived caller context; TypeScript accepts optional
`signal` and `timeoutMs`. There are no fallback method ceilings or invented
forward deadlines. Live binding expiry stays host-owned: it is not on the
ForwardContext wire. Only private fixtures may inject verified expiry metadata.
The host enforces binding and grant policy at admission and before commit.

The helpers use the existing bounded serializer, pending-call registration,
cancellable publication, and typed DTO validation. They never retry mutations,
issue HTTP requests locally, or bypass the host's policy. Storage mutations,
egress with operation keys, and events verify the correlated receipt's operation
key. A mismatched receipt fences correlation and reports `unknown_outcome`.
Validated host `-32010` failures remain typed (`HostRPCError` in Go,
`HostRPCFailure.data` in TypeScript), including host refusal detail. Transport
cancellation and deadline errors remain distinct from application veto; possible
mutation transmission retains `unknown_outcome` and `effect_state: unknown`.

Secrets return owned bytes plus host expiry metadata, with no cache. Before
exposure, their canonical base64 and exact decoded text forms register with the
connection's existing secret tracker. Host log redacts message, field names,
and nested JSON strings/keys before bounded serialization; oversized/deep field
redaction fails closed. This protects SDK logging, not arbitrary author stderr
or printf output.

The shared `host-storage`, `host-secrets`, `host-egress`, and `host-events-log`
fixtures run through Go and TypeScript request scopes and correlation engines.
Their Init offers and local metadata are fixture setup, not production
negotiation or proof of live host authorization.

## Readonly, MCP and renewal

The same client adds five fixed helpers: readonly query, MCP list tools, start a
MCP tool call, cancel that call, and renew the request's binding. They retain
private fixture-only activation.

Readonly query carries the exact host resource/schema version and opaque params;
the result must echo that resource/version. MCP listing returns one bounded
page. Pass the host's opaque `next_cursor` unchanged for another page using the
same grant/server/scope; absence means the end. An empty tools array can still
have a next cursor. There is no automatic pagination or cursor interpretation.

A tool's `tool_binding` comes from host discovery. Pass it unchanged; the SDK
never derives one from a tool name or schema, or retargets a stale binding.
`is_error: true` remains a normal tool result. Authority refusal is a typed RPC
failure. If an operation key was supplied, its result echo must match the key
actually published. Hosts decide which reviewed effects require that key.

Start a call, then wait for its result:

```go
call, err := host.MCPCallTool(ctx, subprocess.MCPCallToolArgs{
    GrantID: grantID, ServerID: serverID, ToolName: tool.ToolName,
    ToolBinding: tool.ToolBinding, Arguments: json.RawMessage(`{"key":"one"}`),
})
if err != nil { return err }
result, err := call.Wait(ctx)
```

```ts
const call = ctx.host.mcpCallTool({
  grant_id: grantID, server_id: serverID, tool_name: tool.tool_name,
  tool_binding: tool.tool_binding, arguments: {key: 'one'},
});
const result = await call.result;
```

The SDK-owned handle exposes no numeric ID and cannot be forged into a valid
call reference. Go's start context owns operation lifetime; `Wait(ctx)` narrows
waiting only and does not restart a call or cancel it. TypeScript's start options
narrow operation signal/budget. To request acknowledged host cancellation, call
`MCPCancelCall(ctx, MCPCancelCallArgs{GrantID: grantID, Call: call})` in Go, or
`mcpCancelCall({grant_id: grantID, call})` in TypeScript. It targets this scope's
plugin-owned call through the existing pending table. Foreign/invalid handles
fail locally. An accepted cancellation does not fabricate a terminal tool result
or promise rollback; wait for the call's own terminal outcome. A completed own
call may return `already_terminal: true`. Transport cancellation is separate.

Renewal selects an explicit existing grant; hosts check the binding's current
grant membership. It returns the same binding reference, verified expiry, and
remaining budgets. One renewal can be outstanding per request scope; overlap
fails locally with `rate_limited/not_started`, without a queue or retry.

The lease end is capped by both the host timestamp relative to the plugin wall
clock at the correlated reply and that reply time plus the **published requested
lease duration**. This duration cap prevents clock skew from extending a lease
indefinitely. The relative remaining timeout is anchored at that same reply,
not at a later wait or call. Observed budget dimensions stay conservative,
including zero and later omission; they are host evidence, not SDK consumption
accounting. All clients on that request observe the updated metadata. Renewal
cannot extend the parent/caller deadline or an existing call's budget, replace a
binding, revive completed/disconnected authority, or infer a successful renewal
from transport failure. Host live policy and commit enforcement remain decisive.
