# Changelog

## [Unreleased]

## [0.2.0] - 2026-11-20

- Subagent links. A subagent's turn record gains `parent_turn_id`,
  `parent_call_id` and `depth`; they need `parent_conversation_id`. A turn
  record gains `subtasks`, the subagents it started (`Subtask`: `id`,
  `variant`, `status`, `background`, `steps`, `usage`), always written, as
  `[]` when empty. `SubtaskStatus` is `running`, `ok`, `partial`, `limit`,
  `error`, `cancelled` or `denied`. Additive: the schema version stays 1.
- `Attributes` adds `agent.turn.subtasks`, the number of subagents started.

## [0.1.0] - 2026-11-19

- First version: the per-turn trace record `Turn` (schema version 1), with
  validation and an OpenTelemetry GenAI attribute view.
- `evidence`: the passive runtime-event evidence recorder (correlations,
  usage and gate receipts, JSON export and import, replay), extracted from
  `agent/sdk/trace` without the SDK operative. Its payload type is
  `evidence.EventPayload`.
