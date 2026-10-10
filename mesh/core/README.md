# mesh

`github.com/hollis-labs/substrate/mesh` holds the contracts and building blocks that
agents, hosts and providers use to talk to each other: who an actor is, what it may
ask of another, how work is assigned and followed, how a team is formed and run, and
how an agent is defined.

## What is in the module

| Package | Purpose |
|---|---|
| `mesh` (root) | The provider contract: actors and URNs, verbs, canonical task, session and instance states with their mapping table, the event envelope, capability descriptors and negotiation, assignment receipts, lookup and authorized replay, versioned results, typed diagnostics, and enrollment, execution and binding records. Standard library only. |
| `mesh/fake`, `mesh/conformance` | An in-memory provider and the reusable conformance suites. Provider implementations run the suites in their own tests. |
| `mesh/teams`, `mesh/teams/memory` | The team model behind host interfaces: slots, phases, authority checks, routing, spawning with limits, launch and recovery, and an in-memory host for tests. |
| `mesh/agentdefv1` | Preserved version-1 parser, layered authoring/skills utilities, generated-span validation, linting and CLI, without changing the version-2 schema. |
| `mesh/agentdef` | Parsing, validation and digests for the version-2 agent definition file. |
| `mesh/agentmuxclient` | Preserved legacy Agent Mux client (package `agentmux`), with its original socket, DTOs and endpoint contracts; new integrations use `mesh/tetherclient`. |
| `mesh/messaging`, `mesh/federation`, `mesh/hitl`, `mesh/tetherclient` | Durable messaging, cross-host federation, human-in-the-loop requests and the HTTP client for the Tether daemon, including explicit remote environment connections and supervised streams. |

Packages under `internal/` are not part of the module's API.

The root package also defines the closed
[`workspace.snapshot.taken.v1` payload](docs/workspace-snapshots.md). Hosts use
verified durable capture and retention receipts before publishing this event;
schema validation does not supply custody, capture authority or quiescence.
Its source-journal interval is distinct from `Event.Cursor`.

## Versioning and stability

Releases are tagged `mesh/vX.Y.Z`. The first release is `v0.1.0`.

While the major version is `0`:

- There is no compatibility guarantee. A minor release (`v0.1.0` to `v0.2.0`) may contain
  breaking changes, and every breaking change is listed under "Changed" in the
  [changelog](CHANGELOG.md).
- New capabilities ship in a minor release. A patch release only fixes bugs and does not
  change the exported API.
- Consumers pin an exact version and adopt a new one on their own schedule. Releases of
  this module are never coordinated with releases of other modules.

Parts of the surface are expected to change once a provider implements them. In
particular the `task.lookup` and `event.follow` verbs, the capability identifiers
`urn:hollis-labs:mesh:dispatch/v1` and `urn:hollis-labs:mesh:spawn/v1`, and the result
schema `urn:hollis-labs:mesh:result/v1` are provisional.

## Install

```sh
go get github.com/hollis-labs/substrate/mesh@v0.4.0
```

The typed Tether client is imported as
`github.com/hollis-labs/substrate/mesh/tetherclient` (package `tether`). Its
[remote environment guide](tetherclient/README.md#remote-environments) describes
explicit credential references, identity checks, stream cursors and caller-owned
mutation retries. The new environment API requires `v0.4.0` or newer.

## Requirements

Go 1.26.9 or newer. The root package imports only the standard library; other packages
bring the third-party modules listed in `go.mod` (for example the SQLite driver used by
`messaging/sqlstore`).

## Development

Build, test and release conventions for all modules of this repository are in the
[repository README](../README.md).
