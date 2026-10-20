# Mapping today's kinds onto the catalog

This table shows how each producer's current event kinds land on the shared bus catalog (`catalog.json`).
It is event stream contract C2 (CW-20261016-0130). Each producer lane implements its own rows.

Every row has one of these outcomes:

- **keep**: the kind is registered in `catalog.json` unchanged.
- **→ X**: the producer emits catalog kind X on the bus. If the old kind was on the bus, it is registered as `deprecated` with successor X, and both are emitted during the deprecation window.
- **local**: the kind stays inside the producer (its own table, SSE or UI channel) and does not go on the shared bus in v1.1. A lane may propose promoting it later; promotion means a catalog PR.
- **name pending**: the kind belongs on the bus, but its current name breaks the naming rule. The producer lane proposes a name in a catalog PR. Until then it stays local.

**Reserved: `stream.*` is transport control, not events.** A `stream.*` signal (such as `stream.gap`, which carries `reason`, `earliest_available` and `high_water`) tells one subscriber about its own delivery. It is never registered in `catalog.json`, never persisted in a durable log, never given an `evt_` id, and never relayed or federated. A transport may carry it in its event frame shape so consumers can switch on the kind; consumers already tolerate kinds they do not know.

When the old kind's meaning is a transition, the producer carries the old detail in the successor's payload: `reason`, `trigger.kind`, and `outcome` for approvals.

Inventory sources, read 2026-10-16:

| Producer | Ref |
|---|---|
| Tether | origin/main d03f6cd |
| Torque | origin/main e42c797 |
| Tangent | origin/main 0dede48 |
| libs | origin/main f8c3000 |
| Tesseract | main a8f5ebf |
| Hadron | main 45c83b4 |
| Cerberus | main 2bf26bc |

## runtimeevents (libs harness)

All 32 kinds in `kinds.go` are kept. `agent.permission_*` and `policy.approval_requested` keep their kinds and are also mapped to `approval.requested` and `approval.resolved` notifications at the bus, as section 6 of the design specifies.

## mesh/core

| Today | Outcome |
|---|---|
| `workspace.snapshot.taken` | keep (its payload schema stays in mesh/core) |
| `Event.Kind` from Tether environmentstream | Passes the durable Tether kind through, so the Tether rows below apply |
| `gap`, `synchronized` (environmentstream SSE control frames) | local: transport control, not events |

## Tether bus (`events` table)

This overlaps CW-20261014-0144, which resolves here. That task now implements these rows on Tether's side, together with CW-20261016-0136 (X1) and CW-20261016-0137 (X2).

| Today | Outcome |
|---|---|
| `session.state_changed` | keep, with payload schema `state_changed@1`. Add `entity` and `trigger` to today's `{from,to,exit_code,reason}`. |
| `session.turn_routed`, `session.turn_output`, `session.boot_dir_planted`, `session.output`, `session.shim_status`, `session.stalled` | keep |
| `session.force_closed`, `session.shim_retired`, `session.launch_failed`, `session.reaper` | → `session.state_changed`, with `reason` set to the old name. `trigger.kind` is `human` for force_closed and shim_retired, `system` for launch_failed and reaper. |
| `session.turn_interrupt_requested` / `_completed` | → `interrupt.requested` / `interrupt.acknowledged` |
| `session.request_status` | → `approval.requested` (open) / `approval.resolved` (close) |
| `session.activity` | → `session.heartbeat` |
| `provider.session_lost` | → `session.lost` |
| `provider.permission_denied` | → `agent.permission_denied` |
| `broker.envelope_created`, `broker.envelope_replied` | → `message.sent` (a reply carries `in_reply_to`) |
| `routing.reply_delivered` / `routing.reply_undeliverable` | → `message.delivered` / `message.failed` |
| (no event today) send, claim, deliver, read, consume, dead-letter, redrive, cancel, archive, purge, wake | → `message.*` and `wake.*` (X2) |
| `daemon.started`, `daemon.shutdown_started`, `daemon.shutdown_completed`, `daemon.shutdown_sessions_ended`, `daemon.sessions_swept`, `daemon.execution_policy_changed` | keep |
| `ai.budget_rejected` | keep |
| `sysop.session_stop`, `sysop.session_checkpoint`, `sysop.logical_agent_resume`, `sysop.session_cleanup`, `sysop.logical_agent_policy` | keep (category audit) |
| `tool_call_start` / `tool_call_end` | → `tool.call.started` / `tool.call.finished` (X3). `ok` appears only on finished. |
| `machine_authority_decision`, `sandbox_rule_unenforced` | name pending (X3 and X4) |

## Torque

| Today | Outcome |
|---|---|
| `task.transitioned` (bus and SSE), `task_transitioned` (run_events) | → `task.state_changed` |
| `run.started`, `run.completed`, `run.canceled`, `run.finished`, `run.superseded`, and `run_*` lifecycle rows (`run_started`, `run_completed`, `run_canceled`, `run_interrupted`, `run_blocked_permanent`, `run_orphan_recovered`, `run_superseded`) | → `run.state_changed`, with `to` taken from the old verb |
| `run.progress`, run_events `progress` | → `progress.checkpoint` (T3; non-parking) |
| `checkpoint.emitted`, `checkpoint.responded`, `checkpoint.canceled`, `checkpoint.timed_out`, `checkpoint.escalated` | → `checkpoint.state_changed` |
| `sprint.transitioned` | → `sprint.state_changed` |
| `plan.phase_added` / `plan.phase_removed` (bus), `plan.phase.added` / `plan.phase.removed` (SSE) | local. Phase lifecycle on the bus is `phase.started`, `phase.completed` and `phase.state_changed` (T2). The two spellings should be unified inside Torque. |
| (no event today) quality gates and landing gate | → `gate.evaluated` (T2) |
| `daemon.up` | → `daemon.started` |
| `health.anomaly` | → `alert.raised` |
| `worker.stale`, `worker.stale.live` | → `fleet.node.degraded` |
| `envelope.created`, `envelope.responded`, `envelope.escalated`, `envelope.delivered` (broker), `envelope.paused` (reactor) | → `message.*` lifecycle, if Torque's broker stays. Otherwise local. |
| `run.event`, run_events `log`, `artifact`, `tokens`, `tool_use`, `unknown` | local (executor detail); `data.emitted` for structured results (T3) |
| `scheduler.tick`, `scheduler.dispatch_skipped`, `task.notify`, `wait.fired`, `recovery.pack_planted`, `checkpoint.response_dispatched`, `checkpoint.orchestrator_redispatched`, `end_agent_enqueue_failed` | local |
| CRUD notifications: `task.created` and `task.updated`, plus the created/updated/deleted events of `artifact.*`, `collection.*`, `epic.*`, `issue.*`, `project.*`, `sprint.*` and `plan.*`; also `judge.changed`, `planner.changed` | local (UI cache invalidation on Torque SSE) |
| `CanonicalEvent` | Not wired. T1 either wires the choke point to emit catalog kinds or retires the type. |
| Plugin-catalog constants with no emit site (`task.transitioning`, `task.assigned`, `run.failed`, `deliverable.check`, `plugin.*`, `config.changed`) | local; no change |

## Tesseract

| Today | Outcome |
|---|---|
| 20 `audit_events` actions (`write`, `typed_write`, `status_promote`, `status_deprecate`, `session_snapshot`, `packet`, `bulk_ingest`, `chunked_ingest`, `promote.*`, `maintenance.*`, `memory.*`, `knowledge.*`, `namespace.*`) | → `audit.recorded`, with `action` set to the action name (O1) |
| (no event today) `touch` | → `audit.recorded`, with `action=touch` (O1) |
| Event-domain types `journal`, `reasoning` | local (content, not events; pull only) |
| Plugin-catalog `context.*`, `embed.completed` (emit sites unconfirmed) | local |

## Hadron

| Today | Outcome |
|---|---|
| `run.status_changed` (workflow_events) | → `run.state_changed`, with `entity.type=workflow_run` |
| `node.attempt_finished` | → `step.executed` |
| Admission `PolicyEvaluation` (appworkflow, including a denial before a run exists) | → `gate.evaluated`, with `gate{id: hadron.admission, kind: custom}` and result `allow` → `pass`, `deny` → `fail`, `confirm` → `blocked`. The subject is the run (`msg://workflow_run/hadron/<run id>`), or the admission request (`msg://workflow_admission/hadron/<evaluation id>`) when no run exists. |
| Human-gate wait (a wait on a human decision) | → `gate.evaluated` with `gate.kind=approval`: `blocked` when the wait starts, then `pass` or `fail` only when the resume carries an explicit decision. A resume without a decision (timeout, cancel) emits nothing global. If go-hitl holds the decision, `approval.*` comes from the go-hitl owner, not from Hadron. |
| other `workflow_events` types (`node.*`, `control.*`, `compensation.*`, `external_operation.*`, `fanout.*`, `retry.*`, `service.*`, `wait.*`, `cancellation.resolved`, `run.cancellation_requested`, `run.terminal_intent_recorded`, `run.replay_created`, `run.fail_fast_triggered`) | local. They are served through Hadron's own subscription with a resume cursor (O2). |
| Legacy pipeline `run_events` (`started`, `completed`, `step_*`, `hook_*` …) | local |

Hadron run and node ids stay in `subject` and the payload: `scope.run_id` and `scope.attempt` are Torque's. The envelope `sequence` is the outbox's own producer-wide counter; the per-run workflow_events sequence goes in the payload as `run_sequence`.

## Cerberus

| Today | Outcome |
|---|---|
| `approval_requested` | → `approval.requested`, with `request_id` the broker id and `hitl_ref` `msg://approval/cerberus/<broker id>` |
| `approval_decided` | → `approval.resolved` (`approved` or `denied`) |
| `approval_expired` | → `approval.resolved` (`expired`) |
| `approval_revoked` | → `approval.resolved` (`withdrawn`) |
| `approval_consumed`, `intent`, `outcome`, `grant_created`, `grant_used`, `grant_revoked`, `break_glass`, `break_glass_acked`, `enforcement_changed`, `brake_changed`, `enrollment_changed`, `presence_helper` | → `audit.recorded`, with `action` set to the record kind and `chain_digest` set (O3) |
| `chain_start`, `file_start`, `chain_break`, `chain_reanchored` | local (integrity of the chain itself); `chain_break` also raises `alert.raised` |
| Schedule notifications: outbox `failure` / `recovery` | → `alert.raised` / `alert.cleared` |
| Schedule notifications: `success`, `misfire` | local |

`approval.*` is derived from Cerberus's approval store, which is authoritative for approvals, and `audit.recorded` from the audit chain. An approval event with no matching audit record raises `alert.raised` (`rule_id=cerberus.audit_gap`); a missing audit record is never synthesized. Audit and scheduling are separate sequence namespaces, told apart by `source.channel` (`audit`, `scheduling`). Schedule alerts whose sources stay local carry empty `source_event_ids` and payload `source_refs`.

## Tangent

| Today | Outcome |
|---|---|
| `interaction.admitted` (approval-class interactions) | → `approval.requested` (O4) |
| `interaction.resolved` / `interaction.canceled` (approval-class) | → `approval.resolved` (`approved` or `denied` / `withdrawn`) |
| other telemetry (`interaction.presented`, `interaction.pending`, `interaction.refused`, `presentation.refused`, `connection.*`, `draft.refused`, `delivery.recorded`, `delivery.failed`, `capability.denied`, `readiness.changed`, `renderer.unavailable`) | local (closed telemetry set) |
| durable `surface_events`, `interaction_events`, `delivery_events` | local |
| WebSocket frames (`connection`, `envelope`, `sync`, `ui.command`, `error`) | local (transport) |

Tangent has no SSE event stream today, so O4 adds the typed v1.1 stream.

## Lifecycle (resource, service, deploy, host)

One vocabulary for every app. Services and managed resources use `resource.state_changed` (payload `resource.state_changed@1`); deploys use `deploy.state_changed`; hosts use `fleet.node.*`; a wedged or degraded resource that needs attention also raises `alert.raised`.

| Event | Kind | Notes |
|---|---|---|
| A managed resource or an app's own service changes state | `resource.state_changed` | `entity.type` is `resource` (something Cerberus manages) or `service` (an app's own daemon: Torque, Hadron, Nanite, Fragments Engine). `to` (and `from`) are one of `stopped`, `starting`, `running`, `deploying`, `degraded`, `wedged`, `failed`, `removed`. |
| Restart, recovery, intentional stop, crash | `resource.state_changed` | Not states. `restarted` is `to=running` with `reason=restart` after `starting`; `recovered` is `to=running` with `reason=recovered` from `degraded`, `wedged` or `failed`; an operator stop is `to=stopped`, `trigger.kind=human`, `reason=operator_stop`, so it never looks like a failure. |
| Wedged (process up, health failing past a threshold window) | `resource.state_changed` `to=wedged` and `alert.raised` | `alert.raised` has `rule_id=<app>.wedged` and `coalesce_key` the resource id; `alert.cleared` with the same key when it ends. `source_event_ids` holds the `resource.state_changed` event id. |
| A deployment starts, ends or is rolled back | `deploy.state_changed` | `entity.type=deployment`; `to` is `started`, `succeeded`, `failed` or `rolled_back`. `corr.correlation_id` ties it to the `resource.state_changed` events (`deploying`, then `running`). `artifact` is a version or digest, never a path with credentials. |
| A host reboots, becomes unreachable or comes back | `fleet.node.restarted`, `fleet.node.down`, `fleet.node.up`, `fleet.node.degraded` | Hosts only. Payload `fleet.node@1` (`environment_id` required). A reboot or an unreachable host also raises `alert.raised` (`rule_id=<app>.host_reboot` or `<app>.host_unreachable`). |
| Daemon process start and stop | `resource.state_changed` with `entity.type=service` | New lifecycle emission uses this kind. The stable `daemon.*` kinds stay as they are (Tether's, and Torque's `daemon.started`), but a producer must not emit both a `daemon.*` kind and `resource.state_changed` for the same transition. |

Subjects follow the existing convention `msg://<noun>/<app>/<id>`: `msg://resource/<app>/<resource id>`, `msg://service/<app>/<service>`, `msg://deployment/<app>/<deploy id>`, `msg://host/<app>/<name>`. The hub stamps `actor` (`msg://service/<app>/<env>`); producers never send it.

Labels in the payload (`labels`: `project`, `host`, `connector`, `mode`, `supervisor`, `run_from`, `env`, `owner`, `admin`) say where and how the resource runs. They never carry configuration values or environment variables.

Producers that own these kinds are listed in `producers.json` (`resource.` for Cerberus, Torque, Hadron, Nanite and Fragments Engine; `deploy.` and `fleet.` for Cerberus).

## Not confirmed

- Emit sites for the Torque and Tesseract plugin-catalog constants.
- Whether the Tesseract event domain writes `event.*` revision audits.
- Exact line numbers for Cerberus `enrollment_changed` and `presence_helper`.
