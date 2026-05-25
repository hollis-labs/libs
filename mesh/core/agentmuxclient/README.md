# go-agentmux-client

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
