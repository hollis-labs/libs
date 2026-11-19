# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-19

- First version: the per-turn trace record `Turn` (schema version 1), with
  validation and an OpenTelemetry GenAI attribute view.
- `evidence`: the passive runtime-event evidence recorder (correlations,
  usage and gate receipts, JSON export and import, replay), extracted from
  `agent/sdk/trace` without the SDK operative. Its payload type is
  `evidence.EventPayload`.
