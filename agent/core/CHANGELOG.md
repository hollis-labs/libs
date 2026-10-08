# Changelog

All notable changes to the `agent` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`agent/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## [0.1.0]

### Added

- Native context windows, token budgets, compaction, overflow classification and handoff mechanics extracted with their source history and behavioral tests.
- Ordered agentcontext assembly, provenance, limits, resolvers and skill discovery, with no harness dependency. Artifact composition remains a caller-owned harness adapter.
- Provider turn observation, native iteration control, bounded tool batches and once-bound approval registry.
- Child lifecycle mechanics with explicit host authorization and a standalone SQLite storage contract.
- Canonical per-run status, durable snapshots, bounded replay and an HTTP SSE writer with host-owned authentication.
- An offline public-API embedding example exercising tools, approvals, retries, cancellation, truncated terminals and database reopen.
