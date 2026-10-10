# Changelog

All notable changes to the `llm-core` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`llm-core/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- `llm-core/quota`: usage limits over explicit windows that never share a counter (calendar day, ISO week or month in a named time zone, following the local wall clock across daylight-saving changes; rolling; provider-reported with a reset time), in native units. `Limiter` offers `Check`, and `Reserve` / `Commit` / `Cancel` for estimate-then-reconcile on streaming responses, answering `Decision{Allowed, Remaining, RetryAfter, Reason}`, with an injectable clock. Stores: `MemoryStore`, `SeededStore` (seeded from the application's own history) and `SQLStore` (reads the application's events table through `database/sql`; never opens a database, writes or deletes rows). `llm-core/quota/quotatest` is a conformance suite for stores. The package README maps `llmcontracts.TokenRateTracker` and the rate and budget helpers elsewhere in the organization onto it; nothing existing changed.

## v0.1.0 — 2026-10-03

First release of the llm-core module: the packages of seven former Hollis Labs modules, moved in with their git history.

### Added

- `llm-core/llmtypes`: Transport-agnostic request, response, tool and stream-event types shared across LLM integrations (from `go-llm-types`, 16 commits of history).
- `llm-core/llmcontracts` and `llm-core/llmcontracts/contracttest`: The provider interface, optional capability extensions, shared types and pure rate-budget helpers for LLM provider adapters (from `go-llm-contracts`, 11 commits of history).
- `llm-core/embedcontracts`: Shared text-embedding interfaces and data shapes (from `go-embed-contracts`, 7 commits of history).
- `llm-core/usageledger`: A disjoint LLM token-usage ledger: a fixed core plus dimensions, per-component provenance and derived totals (from `go-usage-ledger`, 5 commits of history).
- `llm-core/modelsdev`: A cached client for the models.dev API: LLM provider and model metadata with pricing, limits, modalities and capabilities (from `go-modelsdev`, 13 commits of history).
- `llm-core/costcalc`: Cost calculation over disjoint usage components: a `usageledger` Usage priced with `modelsdev` Pricing (from `go-modelsdev-catalog-helpers`, 5 commits of history).
- `llm-core/contracts`, `llm-core/contracts/capabilities` and `llm-core/contracts/runtimes`: The shared contract types for launching an agent, and the capability and runtime vocabularies (from `agent-contracts-leaf`, 12 commits of history).

Each lib keeps its own `CHANGELOG.md` (the history of the old module, as written) and has a `MIGRATION.md` with the old and new import paths.

### Changed

- The old modules' release tags were not carried over; this is the first release of the module.
- Import paths in code, documentation and tests moved to `github.com/hollis-labs/substrate/llm-core/...`, mechanically; no symbol was renamed or changed.
- The package clause of the root of `contracts` changed from `agentcontracts` to `contracts`, to match its directory. Callers that already import it under the alias `agentcontracts` need only the new import path.
- The package of `go-modelsdev` lived in that repository's `modelsdev/` directory and is now `llm-core/modelsdev` itself.
- No third-party requirement was raised: the merged `go.mod` carries the versions the old modules pinned (`github.com/cenkalti/backoff/v5` v5.0.3, `github.com/stretchr/testify` v1.11.1 and its indirect set).
- Code that used to pin a sibling at a tag now builds against the sibling's source in this module, at `main` of its old repository: `llmcontracts` against `llmtypes` (was `go-llm-types` v0.3.0, now v0.5.1), `costcalc` against `modelsdev` (was `go-modelsdev` v0.2.0, now v0.3.0 plus 3 commits) and `usageledger` (was `go-usage-ledger` v0.1.0, now v0.1.0 plus 2 commits).

### Removed

- `status.go` and its tests from `contracts`: `InstanceStatus`, `WaitingReason`, `StoppedReason`, `StoppedCause`, `StoppedDetail`, `A2ATaskState` and `InstanceStatus.ToA2A`. The old seven-state status is superseded by the mesh contracts; the git history of `llm-core/contracts` keeps the file.
