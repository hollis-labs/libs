# Scope: the dimension vocabulary

`scope` (`runtimeevents.Scope`, the same type as `mesh.EventScope`) names *where* an event belongs: environment, project, task, run, agent.
Consumers filter, index and join on it.
This document fixes the field set, the source of truth for each field, and the gaps producers must close before they can stamp it.
It is event stream contract C3 (CW-20261016-0131). The design is in Tesseract: `project/portfolio-architect/workspace/design` / `event_message_stream_contract_2026_10_16`, section 5.

## Rules

1. **Identifiers only.** Every field is a low-cardinality id: an opaque id or a URN, never free text, a tag or a label. Labels go in `class.labels`, and anything else goes in the payload.
2. **The stamp stage fills scope.** Each producer has one choke point where it emits, and the stamp stage there fills `scope` from verified context. A value is never copied from a payload, a request argument or a message body.
3. **Missing is allowed; wrong is not.** If the producer cannot verify a value, it omits the field. `actor.verified` says how the caller was identified. Tether's session-scoped fields count as verified only when `callcontext.Snapshot.SessionResolved` is true.
4. **The set is closed and reviewed.** Each field becomes an indexed column in the durable log (SQLite first, Postgres next), so adding one is a schema migration. Propose additions on the contract lane, not in a producer. Until a field is added, producers put the value in the payload.
5. **Ids keep their owner's format.** Torque ids stay `CW-…` and `EP-…`, Tether session ids stay as Tether mints them, and URNs stay URNs. The stamp stage never rewrites an id.

## Fields and sources of truth

| Field | Meaning | Source of truth | Stamped by |
|---|---|---|---|
| `environment_id` | Environment UUID | Tether `internal/environment` identity (`environmentId` in the descriptor) | Tether hub, Outpost, workers |
| `authority` | Environment authority display name (`^[a-z0-9-]{1,32}$`) | Tether `environment.ValidateAuthority` and the descriptor | Tether hub |
| `worker_id` | Worker that hosts the process | Tether `sessions.worker_id` (blank means hub-local) | Tether, Outpost |
| `host` | Machine host name of the producing process | Producer's OS identity, from config | every producer |
| `project_id` | Project | Tether session row and `Snapshot.ProjectID`; Torque `tasks.project_id` | Tether, Torque |
| `workstream_id` | Tether workstream | Tether session row and `Snapshot.WorkstreamID` | Tether (Torque: gap G3) |
| `epic_id` | Torque epic | Torque `tasks.epic_id` | Torque |
| `sprint_id` | Torque sprint | Torque `tasks.sprint_id` | Torque |
| `plan_id` | Torque plan (a task of kind `plan`) | Torque `tasks.parent_id` of a phase child, or the plan task itself | Torque |
| `phase_id` | Plan phase | Torque `metadata.phase_id` on the plan's child task (`docs/plans-v1.md`) | Torque (gap G4) |
| `task_id` | Torque task | Torque `tasks.id`; Torque `sessions.task_id` for a session bound to a task | Torque; Tether when the launch carries it (gap G1) |
| `run_id` | Torque run | Torque `runs.id` | Torque |
| `attempt` | 1-based attempt number of the task's run | None today (gap G2) | Torque |
| `launch_id` | Tether launch | Tether session row and `Snapshot.LaunchID` | Tether (Torque: gap G1) |
| `agent_urn` | Agent address (`msg://agent/…`) | Tether `Snapshot.AgentURN` | Tether |
| `logical_agent_id` | Tether logical agent | Tether session row and `Snapshot.LogicalAgentID` | Tether (not persisted on events: gap G5) |
| `team_run_id` | Tether team run | Tether `team_runs.run_id` (today only in message metadata, as `team_run`) | Tether (gap G6) |

`session_id` stays at the top level of the envelope, outside `scope`. Provider, runtime, pid and provider session id stay in `process`.
`actor` says *who* caused an event and `scope` says *where* it belongs, so `agent_urn` in scope can differ from `actor.urn`. For example, a reaction service (actor) can update an agent's task (scope).

## Data gaps producers must close first

| Gap | Producer | What is missing | Lands on |
|---|---|---|---|
| G1 | Torque | Runs and tasks do not record the Tether session or launch they run in. Torque's own `sessions` table has `task_id` but uses Torque's session ids, not Tether's, and has no `launch_id`. | Torque lane (emit choke point). Aligns CW-20261012-0062 (launch-time correlation stamp). |
| G2 | Torque | `runs` has no `attempt` column. The attempt is only derivable by counting runs per task. | Torque lane |
| G3 | Torque | No `workstream_id` on tasks or runs. | Torque lane, with the Tether workstream binding |
| G4 | Torque | Phases are not first class: `phase_id` lives in `metadata` and phases have no state. | Torque lane (phase and gate events) |
| G5 | Tether | `events` rows store only id, a legacy `scope` string, `session_id`, time, kind and payload. `LogicalAgentID` exists on the in-memory struct but is not persisted, and no scope field is stored. | Tether lane (persist envelope and projection) |
| G6 | Tether | `team_run` is carried only in message metadata. | Tether lane (message lifecycle events) |
| G7 | Tether | Tool-call events carry `workstream_id` and `project_id` only when the calling session resolves. Unmanaged sessions resolve only to the operator principal. | Tether lane (gateway binding and `tool.call` fixes) |
| G8 | all | Outside Tether's environment-stream wrapper, which adds `environment_id` on the way out, no producer stamps `environment_id` or `host` on the event itself. | Each producer's stamp stage |

Sources checked on 2026-10-16:
- Tether `origin/main` d03f6cd: `internal/callcontext/context.go` and `internal/store` session row.
- Torque `origin/main` e42c797: `internal/persistence/sqlstore/migrations` 001, 023 and 040, and `mcpadapter/plan_tools.go`.
