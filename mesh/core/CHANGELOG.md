# Changelog

All notable changes to the `mesh` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`mesh/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.3.0 — 2026-10-10

### Added

- Closed, versioned `workspace.snapshot.taken.v1` event payload with per-root
  outcomes and observation intervals, policy-relative coverage, bound capture
  identities and explicit coalesced source-journal gaps. Destination replay
  cursors remain independent of source journal positions.
- Bounded strict decoding and envelope validation reject unsupported versions,
  unknown or duplicate keys, numeric overflow and inconsistent coverage. This
  pure contract supplies no capture authority, retention pins or host producer.

## v0.2.0 — 2026-10-09

### Added

- History-preserved `agentdefv1` with the full version-1 parser, layered loading,
  skill-tree resolution/copying, lint, generated-span validation, tests and CLI.
  Version-2 `mesh/agentdef` is unchanged; schemas remain separate without aliases
  or fallback. The command uses released `llm-core/contracts/capabilities`;
  the library retains its caller-supplied capability resolver.
- History-preserved `agentmuxclient`, retaining the `agentmux` package name,
  legacy socket, session/broker/catalog endpoints, event DTOs, API errors and
  messaging adapters. Existing `tetherclient` behavior is unchanged.

### Changed

- Minimum Go version is 1.26.9, including the standard-library security fixes
  absent from 1.26.6.
- `github.com/hollis-labs/go-agentdef` imports move to
  `github.com/hollis-labs/substrate/mesh/agentdefv1` (package `agentdef`).
- The old `github.com/hollis-labs/go-agentmux-client` import moves to
  `github.com/hollis-labs/substrate/mesh/agentmuxclient`; messaging imports now
  use the sibling `mesh/messaging` packages. Consumer adoption is separate.

## v0.1.1 — 2026-10-09

### Fixed

- Messaging's shared request/reply contract waits for subscription registration
  before sending, instead of a scheduling-dependent sleep. Subscription and
  reply failures are reported directly; delayed stores retain the same outcome
  assertions and request deadline.

## v0.1.0 — 2026-10-03

### Added

- A module README stating the v0 stability policy, and the MIT license in the module
  directory so it travels with the module.
- Explicit bounded-spawn capabilities and conformance checks for lifetime child,
  fanout, depth and budget ceilings, parent records and cascade cancellation.
- Per-call work history, accepted-delegation result replies queued for the next
  turn, run-scoped pool identity subsets and children-first termination recovery.

- `agentdef`: strict version-2 definition parsing and validation, namespaced
  extension negotiation, semantic and artifact digests, and conformance fixtures.
  Harness permissions reference profile names; content pins are checked for
  syntax and verified by the host resolver.
- Initial module skeleton: `go.mod` and a package doc.
- Portable mesh provider contracts: actor addresses and kinds, pinned definition
  references, verbs, canonical task/session states, history policies and limits.
- Live capability descriptors and exact version, verb and mode negotiation,
  command/response types, execution instance views and a shared event envelope.
- In-memory provider for host tests, with explicit slot membership, reply-to-sender,
  approval grants, result delivery, bounded spawn lineage and cascading cancellation.
- Reusable MVP provider conformance checks, including refusal of unclaimed verbs,
  plus tests for event attribution, snapshot isolation and concurrent idempotency.
- Portable `teams` configuration and flex-phase compiler, authority checks,
  roster-version routing, bounded spawning and journaled launch recovery.
- Enrolled stable pool identities and ephemeral fresh identities with keyed
  cleanup, host interfaces and an in-memory host for contract tests.
- Test-only composition check for the teams host and mesh provider contracts.
- Shared enrollment, execution instance, session and binding lease records,
  pinned resolution contracts and a glossary of distinct lease and fencing scopes.
- Orthogonal instance and session state projection to task states and the A2A
  wire vocabulary, retaining waiting reasons and connectivity metadata.
- Durable assignment contract with actor-scoped intent keys, atomic receipts,
  pinned team selection, current task lookup and typed admission diagnostics.
- Authorized bounded log replay with atomic snapshot watermarks, explicit
  retention gaps, visibility masking and live waits using opaque cursors.
- Versioned result envelopes binding schema, media type and exact content digest;
  unsupported schemas cannot complete work.
- Fake restart checkpoints, queued delivery and reusable dispatch conformance.
- The complete, history-preserved packages from `go-messaging`,
  `go-federation`, `go-tether-client`, and `go-hitl`, including their tests,
  examples, contracts, and package documentation.

### Changed

- Removed the unused MemberProvisioner.Stop method. Release ends stable sessions
  and their binding leases; Retire also ends ephemeral enrollment.
- Assign requires a caller-scoped idempotency key; ReportResult requires a
  supported versioned result envelope instead of a body-only result.
- Task result content is encoded as bytes to preserve the exact digest input
  across JSON transport and snapshot reads.
- The packages now share the `github.com/hollis-labs/substrate/mesh` module.
  Imports move as follows; no consumer adoption is included in this release
  preparation:

  | Old import prefix | New import prefix |
  |---|---|
  | `github.com/hollis-labs/go-messaging` | `github.com/hollis-labs/substrate/mesh/messaging` |
  | `github.com/hollis-labs/go-federation` | `github.com/hollis-labs/substrate/mesh/federation` |
  | `github.com/hollis-labs/go-tether-client` | `github.com/hollis-labs/substrate/mesh/tetherclient` |
  | `github.com/hollis-labs/go-hitl` | `github.com/hollis-labs/substrate/mesh/hitl` |
- Federation and tether-client now import messaging as a package in the same
  module rather than requiring the former `go-messaging` module.
- Old repository tags are not carried into substrate.
