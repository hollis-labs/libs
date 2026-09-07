# go-tether-client

A typed Go client for the Tether daemon control-plane API — unix/tcp/http(s)
transport, session and event operations, messaging routes, the AI gateway
surface, and the provider-neutral session-bootstrap helper. It is the successor
to `go-agentmux-client`. It speaks to the daemon; it does not implement one,
and it holds no daemon-side policy.

## Start Here

- `README.md` covers transports and the default listen address.
- `MIGRATION.md` maps `go-agentmux-client` call sites onto this module.
- `client.go` owns the transport and the session/event/catalog operations.
- `bootstrap.go` owns `ResolveSessionBootstrap`; its file comment states the
  offline guarantee and the idempotency rule.
- `messaging_store.go` implements `go-messaging`'s `Store` over the daemon's
  HTTP routes.
- `ai_stream.go` owns the AI gateway streaming surface.
- `errors.go` and `types.go` own `APIError` and the wire types.

## Commands

```bash
go vet ./...
go test -race -count=1 ./...
```

CI runs both. Tests use `httptest` and never need a running daemon.

## Boundaries

Bootstrap must work with no Tether running. `ResolveSessionBootstrap` always
returns a usable canonical session identity — minting one when neither the
environment nor an option supplies it — and registering with a reachable daemon
is a best-effort second step reported through `Result.Registered`. A caller
that only needs the ID to inject `SESSION=<id>` into a child never has to check
whether the daemon is up. It is idempotent by construction: the same inputs
resolve the same identity rather than minting a second one
(`TestResolveSessionBootstrap_MintsWhenEnvAndOptionBothEmpty`,
`TestResolveSessionBootstrap_PreassignedViaEnv`,
`TestResolveSessionBootstrap_Reconnect`,
`TestResolveSessionBootstrap_PrivateOfflineBoot`).

Long-lived operations use the caller's context, not the transport timeout.
Streaming a chat or attaching to a session must not die at the HTTP client's
deadline — three separate tests pin this
(`TestLongLivedAIChatStreamUsesCallerContextNotTransportTimeout` and its two
siblings), which is a good sign it has regressed before.

Messaging reads are self-scoped: `Get`, `Inbox`, `Thread` and `Subscribe` send
the caller's URN as an `?as=` query parameter, and the daemon requires it
(`TestHTTPStore_GetAndThread_RequireSelfURN`,
`TestHTTPStore_SendsAsQueryParam`). Dropping it makes the daemon reject the
request rather than returning someone else's mail, but it is easy to miss when
adding a route.

`TestHTTPStore_Contract` runs `go-messaging`'s shared conformance suite. Keep
this client passing it rather than special-casing behavior here.
