# go-agentmux-client

## Replacement and repository retirement

New development uses `github.com/hollis-labs/substrate/mesh/tetherclient` from the released module
`github.com/hollis-labs/substrate/mesh v0.1.0`:

```sh
go get github.com/hollis-labs/substrate/mesh@v0.1.0
```

The successor is the Tether client. Adapt legacy mux names, socket defaults and endpoint contracts explicitly; this does not assert API equivalence or change existing consumer pins.

This final redirect is followed by repository archival. Existing source, tags,
versions and Git history remain available; nothing is deleted. The sections
below describe the preserved standalone implementation.

Deprecated. Use `github.com/hollis-labs/go-tether-client`.

This module tracks the older Agent Mux naming and default socket path:

```text
unix:~/.agent-mux/run/muxd.sock
```

Tether is the successor daemon and client surface. New work should move to:

```go
import tether "github.com/hollis-labs/go-tether-client"
```

Migration notes live in:

- <https://github.com/hollis-labs/go-tether-client/blob/main/MIGRATION.md>

What remains here:

- compatibility for consumers that still import the old module
- the historical `agentmux` package name
- the legacy default socket path

This repo is archived and retained only as a pointer to the successor module.
