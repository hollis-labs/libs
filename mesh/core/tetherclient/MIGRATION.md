# Migration from go-agentmux-client

## Module

- Old: `github.com/hollis-labs/go-agentmux-client`
- New: `github.com/hollis-labs/go-tether-client`

```go
import tether "github.com/hollis-labs/go-tether-client"
```

## Default socket

- Old: `unix:~/.agent-mux/run/muxd.sock`
- New: `unix:~/.tether/run/muxd.sock`

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
2. Let the default socket move to `~/.tether/run/muxd.sock`, or pass an explicit listen address during transition.
3. Replace any direct use of old event-stream fields (`ID`, `Event`) with `Seq`, `Kind`.
4. Prefer `SendTurn` over raw `SendInput` for daemon-managed conversational turns.
5. Adopt the AI gateway methods instead of calling raw daemon endpoints.
