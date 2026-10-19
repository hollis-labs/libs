# Changelog

All notable changes to the `agent` module are documented here (the module was `agent` in the substrate repository). The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`agent/core/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- `reflexspec`: the portable reflex-definition contract (record shape, trigger predicate grammar, per-kind `action_spec` validator and conformance fixtures), imported from the former `reflex-definition-spec` repository with history. It sits beside `reflexes`.

## [0.4.0] - 2026-10-16

### Changed (breaking)

- Module path is now `github.com/hollis-labs/libs/agent/core` (was `github.com/hollis-labs/substrate/agent`); the substrate path and its `agent/vX.Y.Z` tags no longer exist. Tags are `agent/core/vX.Y.Z`. Entries below keep the former package names.
- `agentcontext` (with `resolvers` and `skills`) is no longer part of this module: it moved down to `github.com/hollis-labs/libs/harness/core/agentcontext`, because the harness is a layer of its own below the agent and its launch engine builds on it. Import it from there.
- The permission contract is `github.com/hollis-labs/libs/security/policy/permission` (was `harness/interception/permission`); `approval` and the embed example import it from there.
- Requires `llm/core` v0.2.0, `mesh/core` v0.4.0, `security/policy` v0.1.0 and `ui/ui-go` v0.1.0 (were the substrate `llm-core`, `mesh` and `harness` modules and `libs/ui-go`). The module no longer depends on the harness.

## [0.3.0] — 2026-10-09

### Changed

- Core turn/context streams, approvals, run projections and subagent messaging
  now use the published llm-core, harness permission, ui-go and mesh package
  identities. Consumers must migrate their corresponding standalone imports;
  old and new named types are not interchangeable. Approval decisions, stream
  handling and persistence behavior are unchanged.
- Removed the core module's standalone library requirements. The approval
  registry now explicitly depends on the harness permission package, while
  launch and artifact custody remain host-owned.

## [0.2.0] — 2026-10-09

### Added

- Six standalone libraries, moved into the agent module with their complete Git
  histories: `contextwindow`, `loopdetect`, `reflexes`, `toolbroker`,
  `toolresult` and `toolselect`.
- Per-package migration notes recording old and new import paths, source commits
  and the standalone tags that were deliberately not carried into this module.

### Changed

- Imports within the moved packages now use the agent module paths.
- `contextwindow` uses the consolidated `llm-core/llmtypes` and
  `llm-core/llmcontracts` packages; `toolselect/launch` uses
  `llm-core/contracts`. Public symbols and package clauses are unchanged.

## [0.1.0]

### Added

- Native context windows, token budgets, compaction, overflow classification and handoff mechanics extracted with their source history and behavioral tests.
- Ordered agentcontext assembly, provenance, limits, resolvers and skill discovery, with no harness dependency. Artifact composition remains a caller-owned harness adapter.
- Provider turn observation, native iteration control, bounded tool batches and once-bound approval registry.
- Child lifecycle mechanics with explicit host authorization and a standalone SQLite storage contract.
- Canonical per-run status, durable snapshots, bounded replay and an HTTP SSE writer with host-owned authentication.
- An offline public-API embedding example exercising tools, approvals, retries, cancellation, truncated terminals and database reopen.
