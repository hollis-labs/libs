# Changelog

All notable changes to the `util` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`util/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.4.0 — 2026-10-09

### Added

- Scheduler hardening: durable terminal-fire pruning with non-recreatable
  occurrence identities, per-schedule time zones, anchored intervals and jitter,
  bounded overlap/concurrent dispatch and misfire/catch-up policies, and SQLite
  connection defaults and contention coverage.
- Dependency-free scheduler metrics observations and standard-library slog
  adapters. Observer duration is enqueue latency, not job execution time;
  failures count only after their durable retry/exhaustion outcome commits.
- Native scheduler CI on Linux and macOS, plus a visible advisory Windows job;
  a proposed scheduler v1 freeze checklist coordinated with the util module.

### Changed

- Custom scheduler Store implementations must add pruning and preserve the
  documented policy/claim/identity semantics. See scheduler/MIGRATION.md;
  scheduler conformance now verifies the hardening contracts.

## v0.2.1 — 2026-10-09

### Security

- Upgrade OpenTelemetry APIs, SDKs and trace/metric exporters to v1.45.0,
  log APIs/SDK/exporter to v0.21.0, and gRPC to v1.83.1. These versions address
  GO-2026-4985, GO-2026-5158, GO-2026-5426, GO-2026-6061 and GO-2026-6348.
  The OTel v1.43.0 baggage regression requires v1.44.0 rather than v1.43.0.
- Address the additional current scanner findings: GO-2026-6505 (exporter
  endpoint logging), GO-2026-6615 (log batch busy-spin), and GO-2026-6603,
  GO-2026-6611, GO-2026-6612 and GO-2026-6617 with `golang.org/x/net` v0.60.0.
- Use Go 1.26.9 as the preferred toolchain; retain the Go 1.26.6 language floor.

### Changed

- Follow the upstream OTel SDK's default metric cardinality limit of 2000;
  measurements with additional attribute sets aggregate into
  `otel.metric.overflow=true`. Telemetry options and public Go APIs are unchanged.
- Upgrade the slog bridge to v0.20.0 for the new log API. Consumers calling
  upstream log value constructors directly must migrate to `otel/attribute`;
  OTel log v0.21.0 removes the old `log.Value` and `log.KeyValue` APIs.

## v0.2.0 — 2026-10-09

Adds the Tesseract memory API client, moved in with its git history. No existing package changed.

### Added

- `util/tesseractclient`: An HTTP client for Tesseract's memory API, with an importable fake in `tesseracttest` (from `go-tesseract-client`, 9 commits of history). The package clause stays `tesseract`, as it was before the move. The lib keeps its own `CHANGELOG.md` and has a `MIGRATION.md` with the old and new import paths.

## v0.1.0 — 2026-10-03

First release of the util module: the packages of twelve former Hollis Labs modules, moved in with their git history.

### Added

- `util/sqlite`: SQLite helpers: a driver kit, serialized writes and transaction helpers (from `go-sqlite`, 18 commits of history).
- `util/sqlitebackup`: Point-in-time SQLite backups: take, verify and restore (from `go-sqlite-backup`, 6 commits of history).
- `util/svcerr`: A small, transport-agnostic carrier for service-layer errors (from `go-svcerr`, 7 commits of history).
- `util/transportparity`: Parity assertions for tests that run the same operation over two transports (from `go-transportparity`, 6 commits of history).
- `util/strutil`: String helpers: case conversion, slugs, truncation, inspection and random strings (from `go-strutil`, 7 commits of history).
- `util/otel`: An opinionated OpenTelemetry bootstrap for traces, metrics and logs (from `go-otel`, 29 commits of history).
- `util/sftpsync`: Recursive directory transfer over SFTP (from `go-sftpsync`, 4 commits of history).
- `util/apppaths`: The on-disk layout of an application: data, state, cache and config paths (from `go-apppaths`, 13 commits of history).
- `util/localdaemon`: OS-level primitives for a local daemon process (from `go-localdaemon`, 5 commits of history).
- `util/queue`: A driver-based job queue with in-memory, no-op and SQLite drivers (from `go-queue`, 23 commits of history).
- `util/scheduler`: An application-neutral timed activation engine (from `go-scheduler`, 14 commits of history).
- `util/worktree`: Per-run git worktrees: create, inspect, safely remove and sweep, with a merged-branch helper (ghmerged) (from `go-worktree`, 8 commits of history).

Each lib keeps its own `CHANGELOG.md` (the history of the old module, as written) and has a `MIGRATION.md` with the old and new import paths.

### Changed

- The old modules' release tags were not carried over; this is the first release of the module.
- Import paths in code, documentation and tests moved to `github.com/hollis-labs/libs/util/...`, mechanically; no symbol was renamed or changed.
- Three packages have a package clause that differs from their directory, as they already did before the move: `apppaths` (clause `paths`), `sqlite` (clause `gosqlite`) and `otel` (clause `hotel`).
- One `go.mod` can require only one version of a dependency, so the highest version any imported lib asked for won. These were raised for at least one lib (the version the lib pinned before -> the version now):
  - `github.com/mattn/go-isatty`: v0.0.20 (go-queue, go-sqlite) -> v0.0.24
  - `golang.org/x/net`: v0.54.0 (go-otel) -> v0.58.0
  - `golang.org/x/sys`: v0.26.0 (go-apppaths), v0.42.0 (go-queue, go-sqlite), v0.44.0 (go-otel) -> v0.48.0
  - `golang.org/x/text`: v0.36.0 (go-strutil), v0.37.0 (go-otel) -> v0.42.0
  - `modernc.org/libc`: v1.70.0 (go-queue, go-sqlite) -> v1.77.1
  - `modernc.org/memory`: v1.11.0 (go-queue, go-sqlite) -> v1.12.1
  - `modernc.org/sqlite`: v1.48.1 (go-queue, go-sqlite) -> v1.60.1
- The OpenTelemetry instrumentation scope names of `otel` and `otel/genai` are now their new import paths (`github.com/hollis-labs/libs/util/otel`, `github.com/hollis-labs/libs/util/otel/genai`), as they were the old ones before. Exported traces and metrics carry the new `otel.scope.name`; dashboards or alerts keyed on the old name need updating.
