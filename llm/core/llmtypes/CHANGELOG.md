# Changelog

## v0.5.1 — 2026-10-01

- Value fix: `PhaseThinking` is now `"thought"`, was `"thinking"`. The wire
  already spells it `thought` (ACP's `agent_thought_chunk`, go-agent-wrapper's
  `agent.delta`, the go-runtime-events conventions), so one concept keeps one
  spelling end to end. Nothing consumed v0.5.0's value: it was tagged an hour
  earlier and no module had adopted it.

## v0.5.0 — 2026-10-01

Additive (CW-20260930-0137, CW-20260930-0228). Existing literals keep compiling.

- `StreamEvent.BlockID` and `StreamEvent.Phase`. Every delta of one content
  block carries the same `BlockID` and the next block a different one, so a
  consumer can separate consecutive blocks without provider-specific rules.
  `Phase` classifies a delta as `PhaseNarration`, `PhaseFinal` or
  `PhaseThinking` (new constants). Both are empty when an adapter cannot say.
- A shared stop-reason vocabulary for `Usage.StopReason`: `StopReasonEndTurn`,
  `StopReasonMaxTokens`, `StopReasonToolUse`, `StopReasonTurnLimit`,
  `StopReasonRefusal`, `StopReasonCancelled`, `StopReasonError`.
- `NormalizeStopReason` maps provider spellings onto it. Examples: `length`,
  `max_output_tokens` → `max_tokens`; `stop` → `end_turn`; `tool-calls`,
  `tool_calls` → `tool_use`; ACP `max_turn_requests` → `turn_limit`. It is
  case- and separator-insensitive. A reason outside the vocabulary passes
  through unchanged, so nothing is lost. Each value maps one-to-one onto a
  go-chatstream finish reason.

## v0.4.0 — 2026-09-30

- Added `Usage.CostUSD` (float64), the provider-reported cost of the work a
  `Usage` covers. It is a per-event delta, not a running total, because
  consumers sum usage events per run; an adapter whose CLI reports a cumulative
  cost must emit the difference between events. Zero means no cost was
  reported. Additive: existing `Usage` literals keep compiling.

## v0.3.0 — 2026-05-20

- Added `CacheHint` (`Position`, `Index`) and `ChatRequest.CacheHints` so
  prompt-cache directives travel with the per-call request instead of being
  set on a shared provider singleton. The singleton pattern raced under
  concurrent callers and could drop `cache_control` markers, producing
  degenerate echoed turns with `cache_read=0` (agridd/Nanite FU-13).

## v0.2.0 — 2026-05-10

Public-release prep. No public-API changes; additions only.

- Added `examples/` directory with three runnable demos:
  - `examples/chatrequest` — building a `ChatRequest` with system prompt, message, and tool
  - `examples/streamevent` — iterating `StreamEvent` values and using `IsTurnComplete`
  - `examples/slots` — composing a system prompt from `SlotBlock` regions
- Rewrote `README.md` for a public audience: install snippet, runnable
  quickstart, status banner, godoc link, license line.
- Extended `.gitignore` to keep agent-tooling scratch files out of the
  public tree.

## v0.1.0

Initial alpha release.

- Extracted transport-agnostic LLM data model types from `go-providers`
- Added request, message, tool, stream-event, usage, and capabilities structs
- Added `ChatRequest.EffectiveSystemPrompt()` and `IsTurnComplete()`
