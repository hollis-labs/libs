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

Requires Go 1.26.2 or newer.

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

## Session bootstrap

A launcher/host (e.g. agent-setup) can resolve and register a session's
canonical identity at the launch boundary without requiring Tether to be
running -- `ResolveSessionBootstrap` always returns a usable session id even
when the daemon is unreachable, and `Result.Registered` reports whether the
best-effort daemon registration actually happened. Calling it again with the
same preassigned `SESSION` value is always safe: it never invents a competing
identity.

```go
res, err := tether.ResolveSessionBootstrap(ctx, client, tether.BootstrapOptions{
	// SessionID left empty: resolves from the SESSION env var, or mints
	// a fresh one if that's also unset.
	LogicalAgentID: "agt_worker", // optional: associate with a durable actor
})
if err != nil {
	log.Fatal(err) // only a local mistake (e.g. id-minting failure) reaches here
}
os.Setenv("SESSION", res.SessionID) // inject into the child process regardless of res.Registered
if !res.Registered {
	log.Printf("tether unreachable, continuing offline: %v", res.RegisterErr)
}
```

Shell equivalent (register a preassigned `SESSION` once Tether happens to be
reachable; safe to run unconditionally, including when it isn't):

```sh
curl -s -X POST "http://127.0.0.1:7180/sessions/bootstrap" \
  -H 'Content-Type: application/json' \
  -d "{\"session_id\":\"$SESSION\",\"logical_agent_id\":\"agt_worker\"}" \
  || true  # offline is not a failure -- SESSION is already set locally
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
- session bootstrap (canonical identity resolution + registration, offline-safe)
- attach, wait, input, resize, send turn
- checkpoints
- catalog reads
- legacy broker envelopes
- `go-messaging` store/dispatcher over `/messages/*`
- durable delivery: claim / ack / nack (`ClaimMessage`, `AckMessage`, `NackMessage`)
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

## Asserted caller identity (messaging reads)

Tether's daemon requires every messaging **read** to assert who is asking,
via an `?as=<urn>` query parameter (ADR 0045, same-host trust model). The
client attaches it for you, but it needs to know your identity:

- `Inbox` and `Subscribe` derive it from the recipient you already pass.
- `Get` and `Thread` have no such parameter, so they read it from the
  client-level `WithSelfURN` option and return `ErrSelfURNRequired` if it
  was never set.
- `Send`, `Consume` and `Cancel` do not need it.

```go
client := tether.MustNew("", tether.WithSelfURN("msg://agent/agent-mux/agt_xxxxxxxxxxxx"))
```

Get your URN from `mux registry register --print-urn-only`, or from
`tether_whoami` over MCP.

## Durable delivery

`ClaimMessage` / `AckMessage` / `NackMessage` wrap the claim-ack-nack
cycle. Reach for them when a host must durably accept a handoff before
acknowledging it; `Consume` remains the right call when a single call is
honest about what happened.

```go
env, lease, grantedSeconds, err := c.ClaimMessage(ctx, id, me, tether.ClaimOptions{})
// ... durably accept ...
_, _, err = c.AckMessage(ctx, id, me, lease, delivery.StageHostAccepted)
// ... do the work ...
_, _, err = c.AckMessage(ctx, id, me, lease, delivery.StageConsumed)
```

Pass `lease` back unmodified — it is the bearer credential for the
matching Ack/Nack, and the daemon also checks it belongs to the message
in the path. Honor `grantedSeconds` rather than what you requested; the
daemon clamps. A non-retryable `NackMessage` dead-letters and returns a
**nil** error, so read `RecipientDelivery.Status` for the outcome.

Requires `go-messaging` v0.5.2 or newer.

## Migration

See [MIGRATION.md](./MIGRATION.md) for the `go-agentmux-client` to
`go-tether-client` mapping.

## License

MIT — see [LICENSE](./LICENSE).
