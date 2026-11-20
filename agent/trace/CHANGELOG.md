# Changelog

## [Unreleased]

## [0.2.0] - 2026-11-20

- Subagent links. A subagent's turn record gains `parent_turn_id`,
  `parent_call_id` and `depth`. They need `parent_conversation_id`;
  `parent_turn_id` and `parent_call_id` need `depth`, and `parent_call_id`
  needs `parent_turn_id`. A record with `parent_conversation_id` and depth 0
  does not know its depth (one written before this version).
- A turn record gains `subtasks`, the subagents it started (`Subtask`:
  `conversation_id`, `tool_call_id`, `variant`, `status`, `background`,
  `steps`, `duration_ms`, `usage`), always written, as `[]` when empty.
  `SubtaskStatus` is `running` (background only), `ok`, `partial`, `limit`,
  `error`, `cancelled` or `denied`. Conversation IDs may not repeat.
- `usage` is now documented as the turn's own model requests only; a
  subtask's `usage` is its whole tree's total, so a tree's total is a turn's
  own usage plus its subtasks'.
- Additive: the schema version stays 1. The versioning rule is written
  down: an optional field or a new enum value keeps the version, and readers
  ignore unknown fields.
- `Attributes` adds `agent.turn.subtasks`, the number of subagents started.

## [0.1.0] - 2026-11-19

- First version: the per-turn trace record `Turn` (schema version 1), with
  validation and an OpenTelemetry GenAI attribute view.
- `evidence`: the passive runtime-event evidence recorder (correlations,
  usage and gate receipts, JSON export and import, replay), extracted from
  `agent/sdk/trace` without the SDK operative. Its payload type is
  `evidence.EventPayload`.
