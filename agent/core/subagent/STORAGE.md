# Subagent storage and host ports

The host opens the database and provides the `Database` port. The service and
reaper use SQLite statements against `subagent_runs`; they do not migrate an
application database or require profiles, settings, sessions, or mailbox tables.
`SQLiteSchema` provides the standalone table contract for an embedding database.
An existing host table can satisfy the same contract without applying this DDL.

Each run stores its parent and child identity, prompt, role, mode, timeout,
approval outcome, result, error, retry policy, attempt history, and timestamps.
`last_activity_at` carries the heartbeat used by the reaper. Status transitions
use conditional updates and `RowsAffected` so cancellation, approval, completion,
and reaping cannot overwrite an outcome already claimed by another actor.
The schema permits the exported status and mode constants, including `canceled`.
Indexes by parent and status are useful to hosts that query large histories.

The host supplies `SpawnAuthorizer` before spawning. A missing authorizer refuses
the call before any run row or runner dispatch. A refusal is returned unchanged;
`errors.Is` therefore retains the host's cause. A successful host decision may
bypass the approval envelope, with the existing trust audit event. Lookup errors
discard bypass authority and use the normal approval posture. `InputsJSON` is
passed to the runner as data and never supplies authority.

`SettingsReader` supplies approval requirements and expiry. `ProfileResolver`
resolves executable identities and fails unknown roles before insertion.
`ParentageChecker` enforces the host's depth limit. The host can supply mailbox
delivery, approval envelopes, status observation, and completion reactions; none
requires an application package. The runner owns model selection and verified
definition preparation, and must respect its cancellation context.

Runtime configuration is caller-owned. `SetLivenessResolvers` supplies timeout
and heartbeat callbacks, while the library validates their ranges and retains
the default timeout, heartbeat, and fanout bounds. The library reads no runtime
environment variables. Configure all ports before starting calls or workers.
`SetPanicReporter` connects recovered worker panics to host tracing and observation.
