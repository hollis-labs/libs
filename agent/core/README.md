# Agent

Embeddable native agent mechanisms, with no application or harness imports.
The host supplies models, policy, persistence, tools and lifecycle admission.

| Package | Owns | Host supplies |
|---|---|---|
| `context` | Slot windows, token budgets, compaction, overflow and handoff | Provider and persistence callbacks |
| `agentcontext` | Ordered context slots, provenance, limits, resolvers and skills | Context configuration and resolver inputs |
| `turn` | Provider event observation, watchdog and terminal detection | Stream and observation callbacks |
| `runloop` | Request/consume/settle ordering, retry index and finalization | Preparation, provider, tool and output ports |
| `approval` | Once-bound run/call approval registry across reply facades | Permission engine and authenticated view identity |
| `tooluse` | Bounded concurrent and serial batches, joins and panic recovery | Authorized jobs, execution and panic observer |
| `subagent` | Child lifecycle, fanout, approvals, retries, heartbeat and replies | Explicit spawn authorization, database, profiles, settings and mailbox |
| `service` | Canonical per-run reduction, status, snapshot and bounded replay | Atomic snapshot store and committed output projection |
| `transport/httpstream` | Strict cursors and canonical per-run SSE encoding | Route registration, authentication and authorized subscription |

`runloop.Execute` drives the loop itself. Its ports return directives rather
than delegating the whole loop to the embedding host. `Retry` repeats the same
iteration; `Continue` advances it; interrupted runs terminate without successful
finalization. `turn.ExecuteTurn` can require a provider terminal event; a closed
stream alone does not establish successful completion. The reader defaults to
immediate cancellation. A host that must account for already incurred provider
usage can opt into a bounded drain of the queue observed on cancellation; it
never waits for new producer events and still returns the cancellation cause.

Definitions, model authorization, permission posture and tool grants remain
host decisions. Request metadata and context provenance do not confer authority.
The approval registry binds a decision to one run and call and accepts only the
once scope for a bound prompt. Retained unbound host approvals can continue to
use the permission engine's broader semantics through `RespondRetained`.

`subagent.Spawn` requires a host authorization decision before durable effects.
The database schema and port contract are described in
[subagent/STORAGE.md](subagent/STORAGE.md). The host owns migrations and database
connections; the library reads no environment variables to configure liveness.

The service commits a complete snapshot and event checkpoint through one atomic
host store operation before fan-out. Output must be committed before terminal
publication. Snapshots survive a database reopen; event logs are process-local,
bounded and expire after closure. A recovered unfinished turn becomes a failed
`process_lost` outcome. Expired replay emits a gap; clients retrieve the snapshot
rather than treating a gap or transport EOF as successful completion.

The HTTP writer supplies no tokens, routes or server configuration. The host
must authorize the view and run before subscribing and writing. Off-box binding
and credential provisioning stay host-owned. There is no session event stream
or mesh transport in this module.

Run the offline public-API example from this module:

```sh
GOWORK=off go run ./examples/embed -db /tmp/agent-example.db
```

The example uses fake provider, policy, tool and mailbox ports, and only its
explicitly selected local SQLite database. It exercises a tool approval and
settlement, an iteration retry, child execution, canceled and truncated terminal
outcomes, canonical HTTP replay and persisted snapshots after database reopen.
It makes no model or network calls. `examples/embed/main_test.go` runs it with a
fresh temporary database.

Artifact-bearing context composition stays in an embedding adapter that imports
this module. The agent module has no Harness dependency.
