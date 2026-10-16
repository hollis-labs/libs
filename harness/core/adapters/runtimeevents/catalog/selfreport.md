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
  - **Launch** means one dispatch: the run the scheduler starts, or a session launch request. The value is stored on that run or session. It is never stored on a `launch_profile`, which is a named profile, not a launch.
  - **Template:** Torque copies template fields onto the task at creation, as it does for `launch_profile`. So the template's value is copied onto the task, with its origin recorded (`progress_detail_source=template`). Torque adds that column. At runtime the task's own value counts as `task` unless its recorded origin is `template`.
- **Resolution:** the first value that is set wins, in the order launch, task (by recorded origin), template, then the default `milestone`. An unknown value is skipped rather than treated as `off`, so a typo never silences progress. Use `catalog.EffectiveProgressDetail`.
- **Scope of the setting:** it governs `progress.checkpoint` only. `task.state_changed`, `gate.evaluated`, `phase.*` and `data.emitted` are always emitted.
- **Stamping:** every `progress.checkpoint` carries `detail_level` (the level of the report), `effective_level` and `detail_source` (launch, task, template or default). A consumer can then tell what the producer suppressed.
- **Gating:** a report at level L is emitted only when `effective.Allows(L)` is true.

## The calls

Both calls are MCP tools on Torque, with an HTTP equivalent. Their input and output schemas are in `tools/`.

### `torque_task_progress`

`torque_task_progress` reports progress without parking the task.

- **Unlike `torque_task_checkpoint_emit`, it never parks.** It never changes the task's status. It also never creates a checkpoint and never waits for anyone. This is the gap today: `torque_task_checkpoint_emit` parks a task whose checkpoint mode is blocking, and the SSE broadcast drops its payload.
- **Input fields:** `task_id`, `run_id?`, `level` (default `step`), `label` (at most 80 characters), `summary?` (at most 600 characters), `pct?`, `step?`, `total_steps?`, `data_ref?` and `idempotency_key?`.
- **Result:** `{emitted, event_id?, effective_level, detail_source, suppressed_reason?}`. `suppressed_reason` is `below_detail`, `rate_limited` or `duplicate`. A suppressed report is not an error.
- **Idempotency first, then rate limit.** A repeated `idempotency_key` returns `duplicate`, with the original `event_id`. Otherwise, at most one report per task per level is emitted every 2 seconds, and extra reports return `rate_limited`.
- **Shared state.** Idempotency keys (per task and key) and the rate limit are persisted by the Torque server, never held in a client process. A stdio MCP client proxies these calls to the server over HTTP, as workstream claim already does.

### `torque_task_data_emit`

`torque_task_data_emit` emits structured data as `data.emitted`.

- **Input fields:** `task_id`, `run_id?`, `schema` (`id@version`, required), exactly one of `data` or `artifact_id`, `label?` and `idempotency_key?`. `run_id` and `artifact_id` are Torque's numeric ids passed as strings. The artifact, and the run when given, must belong to `task_id`.
- **Inline data is capped at 16 KiB serialized.** A larger body is first created as a Torque artifact on the same task (`torque_artifact_create`), then referenced by `artifact_id`. An oversized `data` value is rejected with error code `data_too_large`, and the hint names `torque_artifact_create`.
- **Result:** `{event_id, data_ref, duplicate?}`. `data_ref` is what `progress.checkpoint.data_ref` points at.
- `progress_detail` does not gate it.

## One choke point

Every task update by an agent or a workflow goes through one service-layer path in Torque. That path covers the transition call, `torque_task_progress`, `torque_task_data_emit`, subtodo completion and the scheduler. It is the only place that emits `task.state_changed`, `progress.checkpoint` and `data.emitted`.

The choke point does these things:

- **Stamps** `scope` (C3, `scope.md`), `actor` and `corr`.
- **Sets** `trigger.kind` from the caller: `agent`, `human`, `scheduler`, `hook`, `reaction` or `system`.
- **Applies** the `progress_detail` gate.
- **Writes** the event row in the same database transaction as the state change (a transactional outbox). Fan-out (SSE, the hub relay) reads from that row afterwards and is at-least-once, so consumers dedupe on the event `id`.

MCP, HTTP and scheduler paths must not publish these kinds themselves. This is the T1 emit choke point, CW-20261016-0133. Today every status write in Torque goes through `emitTaskTransition` (via TransitionTask, TransitionTaskWithReason, ParkTaskOnCheckpoint and the WriteTx transitions), and T1 wraps that path. Two other paths must go:
- The direct `task.transitioned` SSE broadcast in `httpserver/tasks.go` is replaced.
- The unused `TaskUpdate.Status` write path, which skips the hook, is removed.

## Authorization

Once scope enforcement is on, both calls need task-write scope on `task_id`. This is tracked with the per-tool MCP scopes work.
