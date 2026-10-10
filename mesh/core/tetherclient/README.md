# go-tether-client

[![Go Reference](https://pkg.go.dev/badge/github.com/hollis-labs/substrate/mesh/tetherclient.svg)](https://pkg.go.dev/github.com/hollis-labs/substrate/mesh/tetherclient)

A typed Go client for the Tether daemon control-plane API.

`go-tether-client` is the successor to `go-agentmux-client`. It keeps the
daemon-client boundary explicit: unix/tcp/http(s) transport, typed session and
event operations, messaging routes, and the newer Tether AI gateway surface.

**Status:** pre-1.0 (`v0.x.y`). Breaking changes may still occur in minor
versions; see [CHANGELOG.md](./CHANGELOG.md).

## Install

```bash
go get github.com/hollis-labs/substrate/mesh/tetherclient
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

	tether "github.com/hollis-labs/substrate/mesh/tetherclient"
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

## Remote environments

`NewEnvironmentClient` takes an explicit environment record: stable
`environmentId`, an authority display name, ordered HTTP(S) `baseURL` routes,
and a credential reference. It never consults the local catalog, default
socket, `TETHER_TOKEN`, or the operator's default token file. Resolve the
reference through the caller's secret store; keep the secret out of the record.

```go
remote, err := tether.NewEnvironmentClient(tether.EnvironmentTarget{
    EnvironmentID: "env-worker",
    Authority: "Worker environment",
    Routes: []tether.EnvironmentRoute{
        {BaseURL: "https://worker.example.test"},
        {BaseURL: "https://relay.example.test"},
    },
    CredentialReference: "worker-device-credential",
}, tether.EnvironmentOptions{
    ResolveCredential: resolveCredential, // func(context.Context, string) (string, error)
    SelfURN: callerURN,                    // needed for messaging reads
})
if err != nil { return err }

connection, err := remote.Connect(ctx)
if err != nil { return err }
// connection.Client offers the ordinary typed operations on the verified route.
```

Connection discovery reads `/.well-known/tether/environment` anonymously with
a 2.5-second timeout and verifies the configured environment ID and protocol
`1` before resolving or sending a credential. Answered routes are attempted in
preference order; silent routes get a deferred second descriptor pass with a
15-second deadline. A deferred route must still verify identity before
authentication. Caller cookie jars are excluded. No redirect is followed, and
authenticated requests are bound to the selected scheme and host. Supplied HTTP transports must be
credential-free: an arbitrary caller transport must not inject a bearer or
other secret into public discovery. Use HTTPS routes when transport identity
and confidentiality are required; the public descriptor supplies the
environment coordinate, not a new grant.

The authenticated preflight uses `GET /auth/context`, which requires the
credential's **read** scope. Snapshot and event-stream reads have the same
requirement. `operate` and `admin` do not imply `read`. The authority name and
`SelfURN` do not grant scopes. `*ProtocolMismatchError` stops the route walk
and carries `RequiredProtocol` and an advisory `UpdateHint`.
`*EnvironmentIdentityError` refuses a wrong environment;
`*EnvironmentAuthenticationError` identifies a rejected or unresolved
credential. Connection errors exclude raw transport errors and response
bodies; the library does not log credentials.

### Supervised environment and session streams

```go
subscription, err := remote.Subscribe(ctx, tether.EnvironmentStreamOptions{
    SessionID: sessionID, // empty subscribes to the entire environment
    // nil AfterSeq obtains a snapshot first; &zero explicitly replays from 0.
})
if err != nil { return err }
defer subscription.Close()
for update := range subscription.Updates {
    switch {
    case update.Gap != nil:
        // Notify the caller that its projection is incomplete.
    case update.Snapshot != nil:
        // Replace the projection with this snapshot.
    case update.Event != nil:
        // Apply the mesh event. Unknown kinds and payloads remain available.
    }
    // Persist update.State.AfterSeq only after processing the update.
    // Transport and Freshness are separate: connected is not yet fresh.
}
return <-subscription.Errors // terminal error, including caller cancellation
```

Subscriptions resume with the last delivered `after_seq` and suppress
duplicate or older sequence numbers. A `gap` reaches the caller before a
replacement snapshot is fetched; only the snapshot's high-water cursor resets
the subscription. The `synchronized` marker advances the global environment
cursor even for a filtered session stream. Stream sequence numbers are not
provider cursors or raw terminal byte offsets. Shell sessions use the same
durable session-event stream; raw `AttachSession` output remains a separate
surface. Snapshot fields distinguish unknown pending-question/approval state
from an observed clear state.

`State.Transport` reports connecting, connected, disconnected, offline, or
blocked. `State.Freshness` reports unknown, catching_up, fresh, stale, or gap;
only synchronization marks data fresh. Updates apply backpressure without
dropping events. A cursor is committed when its update reaches the consumer,
not when the consumer durably stores it, so callers must persist progress after
processing and resume a new subscription from that persisted cursor after a
process restart. This is sequence deduplication within a subscription, not an
exactly-once application transaction.

Transient disconnects use jittered exponential backoff (1-second base,
5-minute cap), reset after a connection stays established for 30 seconds.
Authentication failure waits for `subscription.Wake()` after credential
rotation. `SetOnline(false)` pauses connection attempts until an online wakeup.
A fallback connection preflights better routes every minute and on `Wake()`;
it authenticates a preferred route before switching. Answering routes that
fail authentication/transport establishment receive a 5-minute cooldown.
Protocol mismatch or an identity failure on the active stream/snapshot
terminates the subscription. A wrong-environment preferred route is refused
without sending a credential or replacing a verified fallback. `EnvironmentOptions`
allows an injected clock and jitter for deterministic supervision tests.
Credential resolvers and supplied transports must respect cancellation.
Resolvers, clocks, transports, and jitter callbacks must be safe for concurrent
subscriptions.

Stream lifetimes use caller context; establishment and supervised snapshots
have bounded deadlines. Ordinary operations on `connection.Client` use the
connection timeout; its existing long-lived methods use caller context.
`Connect`, `Snapshot`, and supervised reads may contact several routes.
Mutations are **never automatically replayed** by supervision. A transport
failure can leave a mutation's outcome unknown: use a caller-owned
idempotency key where the daemon supports it, and decide whether to retry
explicitly. The caller's own HTTP transport must also honor this rule.

For standard `*http.Transport` clients, remote mutations use a separate fresh
HTTP/1 connection. This prevents Go from internally replaying a keyed POST on
a reused connection or retrying an HTTP/2 refused stream. Read requests retain
the supplied transport's connection reuse and protocol settings; the supplied
transport is cloned for mutations. Opaque custom RoundTrippers must themselves
avoid mutation retries.

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
- `SubscribeChannel`
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

## Named channel consumers and routing capabilities

`ListChannels(ctx)` returns the stable channel names and their derived
`msg://service/local/channel/<name>` addresses. `ChannelMessages(ctx, name,
ChannelMessagesOptions{Since: cursor, Limit: 100})` returns history in ascending
sequence order, plus `NextSince`. Set `Last: N` instead of `Since`/`Limit` to
read the latest N publications, oldest first. Purged items carry `Purged` and
`PurgedAt`, including on SSE replay. The cursor is exclusive and durable; sequence
gaps are valid. Channel membership is not required. Configure `WithSelfURN` for
an asserted caller, or a bearer credential for a verified caller.

```go
zero := int64(0) // nil starts live; &zero replays all history
messages, streamErrors, err := client.SubscribeChannel(ctx, "ops", &zero)
if err != nil {
    return err
}
for message := range messages {
    // Save message.Seq after processing; use it to reconnect explicitly.
    fmt.Println(message.Seq, string(message.Payload))
}
if err := <-streamErrors; err != nil {
    return err
}
```

Cancel `ctx` to stop the stream. Initial HTTP errors are returned directly;
stream decoding and transport errors arrive on `streamErrors`. A disconnected
stream closes both channels. An incomplete SSE frame is discarded for replay
on reconnect. Frames up to 16 MiB per line are supported. `SubscribeChannel` does not
auto-reconnect; the consumer resumes explicitly with the last received `Seq`.

`RoutingCapabilities(ctx, "")` discovers gateway-wide support;
`RoutingCapabilities(ctx, sessionID)` narrows the answer to that session. The
answer carries `route_supported`, `reply_to_sender`, `interrupt`,
`kinds_available`, `delivery`, and a `runtimes` map keyed by runtime/provider ID.
Each runtime entry includes `final_text_confidence`. Availability describes the
daemon's currently wired paths, so check it rather than inferring support from
a runtime name. Delivery is `next-turn`.

`Reply(ctx, msgID, body, ReplyOptions{Interrupt: true, IdempotencyKey: "key"})`
queues a reply as the publishing session's next turn. Configure `WithSelfURN`
or a bearer credential for caller identity. The 202 `ReplyReceipt` confirms
acceptance, not delivery; `Interrupt` reports the cancel outcome and `Duplicate`
identifies a replay of the same key. The key goes in the `Idempotency-Key` header.
The client does not automatically retry. Reuse the same key and body for a safe
explicit retry.

Refusals are `*APIError`; use `errors.Is(err, &APIError{Code:
CodeInterruptUnsupported})` or `errors.As` to inspect the status and code.
409 `interrupt_unsupported`, `turn_not_yet_started` and `turn_feed_unavailable`
mean nothing was queued. 413 `payload_too_large` and 400 `invalid_request` /
`reply_target_not_a_session` retain the daemon's typed error. The daemon limits
reply text to 128 KiB and request JSON to 1 MiB.

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

`ReplyDelivery(ctx, receipt.ReplyID)` reads the current delivery record (200),
including state, reason, attempts and timestamps. `ReplyPending`, `ReplyQueued`
and `ReplyDelivering` are nonterminal; `ReplyDelivered` and `ReplyUndeliverable`
are terminal. Exported `ReplyReason*` constants describe outcomes such as
`ReplyReasonHandedOff`, `ReplyReasonTurnFailed` and
`ReplyReasonInterruptUnconfirmed`. A delivered `turn_failed` reply was injected
and must not be sent again. Unknown IDs return a typed 404 `APIError`.
