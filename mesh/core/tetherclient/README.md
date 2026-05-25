# go-tether-client

[![Go Reference](https://pkg.go.dev/badge/github.com/hollis-labs/go-tether-client.svg)](https://pkg.go.dev/github.com/hollis-labs/go-tether-client)

A typed Go client for the Tether daemon control-plane API.

`go-tether-client` is the successor to `go-agentmux-client`. It keeps the
daemon-client boundary explicit: unix/tcp/http(s) transport, typed session and
event operations, messaging routes, and the newer Tether AI gateway surface.

**Status:** pre-1.0 (`v0.x.y`). Breaking changes may still occur in minor
versions; see [CHANGELOG.md](./CHANGELOG.md).

## Install

```bash
go get github.com/hollis-labs/go-tether-client
```

Requires Go 1.22 or newer.

## Default transport

Passing an empty listen address uses:

```text
unix:~/.tether/run/muxd.sock
```

Supported listen address forms:

- `unix:/absolute/path`
- `tcp:127.0.0.1:7180`
- `http://host:port`
- `https://host:port`

## Quick start

```go
package main

import (
	"context"

	tether "github.com/hollis-labs/go-tether-client"
)

func main() {
	ctx := context.Background()
	client := tether.MustNew("")

	_, _ = client.Health(ctx)
	_, _ = client.ListLaunches(ctx)
}
```

## AI gateway example

```go
res, err := client.AIChat(ctx, tether.ChatRequest{
	Request: tether.AIRequest{
		Operation: "chat",
		Input: []tether.AIMessage{{
			Role: "user",
			Parts: []tether.AIContentPart{{
				Type: "text",
				Text: "hello",
			}},
		}},
	},
})
```

## Surface

The client covers:

- health
- session lifecycle
- attach, wait, input, resize, send turn
- checkpoints
- catalog reads
- legacy broker envelopes
- `go-messaging` store/dispatcher over `/messages/*`
- event history and SSE event streaming
- AI providers, models, routes, preview, explain
- AI chat, chat stream, usage, budgets, audit

Long-lived calls use caller context rather than the default short transport
timeout:

- `AttachSession`
- `WaitSession`
- `SendTurn`
- `AIChat`
- `AIChatStream`
- `StreamEvents`

## Migration

See [MIGRATION.md](./MIGRATION.md) for the `go-agentmux-client` to
`go-tether-client` mapping.

## License

MIT — see [LICENSE](./LICENSE).
