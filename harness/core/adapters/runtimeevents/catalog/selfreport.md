# Progress detail and agent self-report

This is event stream contract C4 (CW-20261016-0132). The design is in Tesseract: `project/portfolio-architect/workspace/design` / `event_message_stream_contract_2026_10_16`, section 7.
Torque implements it in the self-report lane, CW-20261016-0135 (T3).
Torque owns the tool surface, with torque-orch; this document fixes the contract.

## `progress_detail`

| Value | Emits `progress.checkpoint` for |
|---|---|
| `off` | nothing |
| `milestone` (default) | phase boundaries and task completion |
| `step` | the above, plus each subtodo and each declared step |
| `verbose` | the above, plus tool-level progress and heartbeats |

- **Where it is set:** on a launch, a task or a template. The schema is `tools/progress_detail@1.setting.schema.json`.
- **Resolution:** the first value that is set wins, in the order launch, task, template, then the default `milestone`. An unknown value is skipped rather than treated as `off`, so a typo never silences progress. Use `catalog.EffectiveProgressDetail`.
- **Scope of the setting:** it governs `progress.checkpoint` only. `task.state_changed`, `gate.evaluated`, `phase.*` and `data.emitted` are always emitted.
- **Stamping:** every `progress.checkpoint` carries `detail_level` (the level of the report), `effective_level` and `detail_source` (launch, task, template or default). A consumer can then tell what the producer suppressed.
- **Gating:** a report at level L is emitted only when `effective.Allows(L)` is true.

## The calls

Both calls are MCP tools on Torque, with an HTTP equivalent. Their input and output schemas are in `tools/`.

### `torque_task_progress`

`torque_task_progress` reports progress without parking the task.

- **It never changes the task's status.** It also never creates a checkpoint and never waits for anyone. This is the gap today: `torque_task_checkpoint_emit` parks a task whose checkpoint mode is blocking, and the SSE broadcast drops its payload.
- **Input fields:** `task_id`, `run_id?`, `level` (default `step`), `label` (at most 80 characters), `summary?` (at most 600 characters), `pct?`, `step?`, `total_steps?`, `data_ref?` and `idempotency_key?`.
- **Result:** `{emitted, event_id?, effective_level, detail_source, suppressed_reason?}`. `suppressed_reason` is `below_detail`, `rate_limited` or `duplicate`. A suppressed report is not an error.
- **Rate limit:** at most one emitted report per task per level every 2 seconds. Extra reports return `rate_limited`. A repeated `idempotency_key` returns `duplicate`, with the original `event_id`.

### `torque_task_data_emit`

`torque_task_data_emit` emits structured data as `data.emitted`.

- **Input fields:** `task_id`, `run_id?`, `schema` (`id@version`, required), exactly one of `data` or `artifact_id`, `label?` and `idempotency_key?`.
- **Inline data is capped at 16 KiB serialized.** A larger body is first created as a Torque artifact on the same task (`torque_artifact_create`), then referenced by `artifact_id`. An oversized `data` value is rejected with error code `data_too_large`, and the hint names `torque_artifact_create`.
- **Result:** `{event_id, data_ref, duplicate?}`. `data_ref` is what `progress.checkpoint.data_ref` points at.
- `progress_detail` does not gate it.

## One choke point

Every task update by an agent or a workflow goes through one service-layer path in Torque. That path covers the transition call, `torque_task_progress`, `torque_task_data_emit`, subtodo completion and the scheduler. It is the only place that emits `task.state_changed`, `progress.checkpoint` and `data.emitted`.

The choke point does these things:

- **Stamps** `scope` (C3, `scope.md`), `actor` and `corr`.
- **Sets** `trigger.kind` from the caller: `agent`, `human`, `scheduler`, `hook`, `reaction` or `system`.
- **Applies** the `progress_detail` gate.
- **Writes** the durable event before any fan-out.

MCP, HTTP and scheduler paths must not publish these kinds themselves. This is the T1 emit choke point, CW-20261016-0133.
