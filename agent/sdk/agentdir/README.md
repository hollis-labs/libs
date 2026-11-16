# Agent directory host

`agentdir` is an experimental host-side layer for Tradecraft. An agent directory
contains `agent.md` (agentdef v2) and `host.yaml` (kind and lifetime). Optional
`.mcp.json` is parsed and handed to the host; it is never executed merely by
loading the directory. Authored files are read on every turn, so editing a file
changes the next turn without changing a database row. The host's conversation
store is a projection, not the definition source.

| Kind | Host profile | Conversation and lifetime |
| --- | --- | --- |
| stable | `on-demand`, `seat_id` | Durable conversation for a seat; can boot on demand. |
| task | `until-task-end`, `task_id` | One durable conversation per task and definition; `EndTask` cancels its active turn and refuses later input. |
| embedded | `application`, `application_id` | Application-scoped conversation; uses the same `Host.Turn` API in-process. |

Stable and task definitions require `continuity.mode: durable`. Embedded
definitions may be durable or ephemeral, with the application owning the
record. `concurrent` is not yet supported. Invalid combinations fail before a
conversation is acquired.

The host supplies a `Store` for authoritative conversation IDs, terminal task
state and one input writer; a `Factory` creates the runner. `SDKFactory` adapts
that runner to `sdk.New` and `Operative.Run`, using host-issued ports and input.
The SDK does not issue identities, grants, environment receipts or leases.
`MemoryStore` is only a local reference projection; production hosts must use
a durable store and transcript writer. The JSONL transcript port is owned by
the chat host and can be supplied through SDK ports when available. This
package does not create a second transcript store.

`Host.Handler()` exposes `POST /agents/{name}/turns` with native
`chatstream/v1` SSE events. It sends the completed answer as one text delta;
the richer incremental stream and cursor replay require the transcript host.
Authentication and authorization belong to the embedding HTTP server.

`examples/agents/` contains valid stable, task and embedded directories. A
host can load the same files for in-process or HTTP use. Packaged executable
skills and hooks are rejected by the current SDK; a host must explicitly
negotiate them before it can run definitions that require them.
