# go-agentmux-client

Deprecated Go client for the Agent Mux daemon, package name `agentmux`. It is
retained only so existing consumers of this import path keep compiling; the
successor is `github.com/hollis-labs/substrate/mesh/tetherclient`. Nothing new should be
built here.

## Start Here

- `README.md` states the deprecation and links the migration notes.
- `client.go` is the whole client surface; `types.go` holds the DTOs and
  `errors.go` the typed API errors.
- `messaging_store.go` adapts `go-messaging` onto the mux broker endpoints.

## Commands

```bash
go test ./...
go vet ./...
```

## Boundaries

This module tracks the older Agent Mux naming and the legacy default socket
path `unix:~/.agent-mux/run/muxd.sock`. Those are the compatibility surface —
changing them defeats the only reason the module still exists. Point new work
at `go-tether-client` instead, and send behavior changes there.

This is now a package of the mesh module. Keep its legacy surface separate
from the modern sibling tetherclient: no aliases, implicit credential discovery,
new socket defaults or silently changed endpoint semantics. Run checks from the
module directory with `go test ./agentmuxclient` and `go vet ./agentmuxclient`.
