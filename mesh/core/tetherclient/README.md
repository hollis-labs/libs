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

Requires Go 1.26.6 or newer.

## Default transport

Passing an empty listen address uses:

```text
unix:~/.tether/run/tetherd.sock
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

## Caller credentials

`New` resolves a bearer credential in this order: `WithTokenFile(path)`,
`WithToken(token)`, `TETHER_TOKEN`, then `~/.tether/run/operator.token`.
An explicitly selected file takes precedence over a token option regardless of
option order. A missing default file keeps the client anonymous, so offline
bootstrap still works. `WithToken("")` explicitly disables automatic lookup.
When `TETHER_MCP_TOKEN` is non-empty, it marks a session or anonymous proxy:
without an explicit token/file option or non-empty `TETHER_TOKEN`, the client
stays anonymous and never reads `operator.token`. The marker itself is not a
daemon credential.

```go
client, err := tether.New("", tether.WithTokenFile("/private/service.token"))
```

Clients read files once during construction and never create or repair them.
On POSIX platforms, a file must be a regular file owned by the current user with
exactly 0600 permissions. Symlinks, special files, loose permissions, malformed
content and a missing explicit file fail `New` without falling back to another
credential. Recreate the client after rotating a token file. On other platforms,
use `WithToken` or `TETHER_TOKEN`; an existing token file is refused because POSIX
ownership and mode checks cannot be established.

The credential is sent as `Authorization: Bearer` on ordinary, bootstrap,
long-lived and streaming requests. The client copies a supplied `http.Client`
and preserves its timeouts and redirect policy. Authenticated requests refuse
redirects to another origin before sending anything there. Credentials are
opaque to the library; the daemon verifies them and applies its configured
identity mode. `WithSelfURN` still supplies the messaging address; it does not
establish verified identity.

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
- session lifecycle, including idempotent create/launch and `ResumeLogicalAgent`
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
- the agent registry: `Client.Registry()` (register, lookup by URN or substrate id, search, update, deregister, merge, sync)
- group messaging: `Client.Groups()` (groups, membership, sequenced messages, read cursors, mentions)
- workstreams and digests, flat on `Client` (`CreateWorkstream`, `EnsureSessionWorkstream`, `SessionDigest`, `WorkstreamDigest`, ...)

The registry, group and workstream methods return `*APIError` like the rest
(`errors.Is(err, &APIError{StatusCode: 404})`). A group send whose @-mention is
ambiguous returns a `*GroupAmbiguousMentionError` carrying the candidate URNs.

### Idempotent launch and resume

Set `LaunchRequest.IdempotencyKey` (or `ResumeOptions.IdempotencyKey` for
`ResumeLogicalAgent`) and a retry returns the session already bound to that
key instead of starting a second one. Every create, launch and resume response
carries `Replayed`; create and resume answer 201 when fresh and 200 on a replay,
and the client accepts both. A replay reports the bound session as it stands
now, including one that has failed or stopped. Reusing a key for a different
request is an `*APIError` with `Code == CodeIdempotencyConflict`. Keys are one
global space, so namespace them yourself (for example `"<app>/<job-id>"`).

Long-lived calls use caller context rather than the default short transport
timeout:

- `AttachSession`
- `WaitSession`
- `SendTurn`
- `AIChat`
- `AIChatStream`
- `StreamEvents`
- `ResumeLogicalAgent`

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

Get your URN from `tether registry register --print-urn-only`, or from
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
