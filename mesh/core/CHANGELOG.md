# Changelog

All notable changes to the `mesh` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`mesh/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

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
