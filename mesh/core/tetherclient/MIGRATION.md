# Migration from go-agentmux-client

## Substrate relocation

`go-tether-client` now lives in the `mesh` module of the substrate repository.

| Old import prefix | New import prefix |
|---|---|
| `github.com/hollis-labs/go-tether-client` | `github.com/hollis-labs/substrate/mesh/tetherclient` |

Replace the prefix for the root package and every subpackage. Package names,
APIs, and the repository's internal layout are unchanged by this move. Its
go-messaging dependency is now the sibling
`github.com/hollis-labs/substrate/mesh/messaging` package, so it no longer
requires another Hollis Labs module. The source history is preserved, but old
repository tags are not carried into the monorepo. The first consolidated
release is planned as `mesh/v0.1.0`.

No consumer import is changed as part of the relocation.

## Module

- Old: `github.com/hollis-labs/go-agentmux-client`
- New: `github.com/hollis-labs/substrate/mesh/tetherclient`

```go
import tether "github.com/hollis-labs/substrate/mesh/tetherclient"
```

## Default socket

- Old: `unix:~/.agent-mux/run/muxd.sock`
- New: `unix:~/.tether/run/tetherd.sock`

## Direct mappings

- `agentmux.New` -> `tether.New`
- `agentmux.MustNew` -> `tether.MustNew`
- `Health`, `Ping` -> unchanged
- `CreateSession`, `LaunchSession`, `Launch`, `ListSessions`, `GetSession`, `StopSession`, `ResizeSession`, `SendInput`, `AttachSession`, `WaitSession` -> unchanged
- `CreateCheckpoint`, `ListCheckpoints` -> unchanged
- `CreateEnvelope`, `ListEnvelopes`, `GetEnvelope`, `ReplyEnvelope` -> unchanged
- `AsStore`, `AsDispatcher` -> unchanged
- `ListProjects`, `ListAgents`, `ListProviders`, `ListLaunches` -> unchanged

## Expanded session surface

- New: `CreateSessionWithBootPrompt`
- New: `CreateSessionWithInput`
- New: `LaunchWithInput`
- New: `SendTurn`
- New: `ListEvents`
- `StreamEventsOptions` now accepts `Kinds`
- `EventListOptions` now accepts `SinceSeq`, `Scopes`, `Kinds`

## AI gateway surface

New methods:

- `ListAIProviders`
- `ListAIModels`
- `ListAIRoutes`
- `PreviewAIRoute`
- `ExplainAIRoute`
- `AIChat`
- `AIChatStream`
- `AIAudit`
- `AIUsage`
- `AIBudgets`

## Event stream shape

`StreamEvent` now uses Tether naming:

- `ID` -> `Seq`
- `Event` -> `Kind`

## Recommended migration sequence

1. Swap the module import.
2. Let the default socket move to `~/.tether/run/tetherd.sock`, or pass an explicit listen address during transition.
3. Replace any direct use of old event-stream fields (`ID`, `Event`) with `Seq`, `Kind`.
4. Prefer `SendTurn` over raw `SendInput` for daemon-managed conversational turns.
5. Adopt the AI gateway methods instead of calling raw daemon endpoints.

## Tether public rename (CW-20261001-0653)

The renamed daemon emits only `tether_instance_id`. Replace uses of
`RegistryProfile.MuxInstanceID` with `RegistryProfile.TetherInstanceID`; no
legacy Go field or JSON alias is provided. The default is now
`unix:~/.tether/run/tetherd.sock`. Explicit old socket addresses must be updated
at cutover. Stored `msg://agent/agent-mux/...` URNs retain their authority;
lookup routing continues to accept those identities (CW-20261001-0624).

Release this client before or alongside the renamed daemon. An older client
silently drops the new instance field, and its default socket reaches the old
socket name. This release targets the renamed daemon; it does not decode the
old instance key.
