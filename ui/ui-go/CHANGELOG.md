# Changelog

All notable changes to the `ui-go` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`ui-go/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.4.0 — 2026-10-16

### Added

- `streamhub/retention`: `Planner` and `Plan`, the store-agnostic half of the retention janitor. `NewPlanner(policy).Plan(now)` turns the current policy into set specs per class (`RollupSpec`, `TombstoneSpec`, `TrimSpec`, `PruneSpec`) that any store can run. Pure, deterministic, no I/O. The current configuration governs every row (a shorter age reaches existing rows on the next pass; nothing is ever restored), and R-audit is never planned for trim, whatever its configuration (D7 rev 2.2).
- `streamhub/retention`: `Store`, the small set-based interface a log or hub implements so the janitor can run on it (`Classify`, `Rollup`, `Tombstone`, `Trim`, `Prune`, `Stats`, `TryLock`, `Caps`), with `TrimHeadOnly` and `TrimPerRow` modes, and `Janitor` (`NewJanitor`, `Run`, `RunFrom`): classify, roll up closed buckets, tombstone payloads, trim, prune rollups, in short bounded store calls (`WithMaxTxRows`, `WithMaxTxDuration`, `WithBudget`), idempotent and resumable, dry-run capable, no goroutines, timers or vacuum. `Report` (per class: scanned, classified, tombstoned, rolled up, trimmed, pruned, bytes freed, backlog, oldest remaining, errors) and its metadata-only `Summary`.
- `streamhub/logkit`: `Columns.Outcome`, lifted by `EnvelopeExtractor` from the payload's `outcome` (or, failing that, `to`), the low-cardinality result retention rollups count by.
- `streamhub/retention`: D7 rev 2.3. `Store.Watermarks` (the persisted per-class watermark W_C of a `TrimPerRow` store: every row of the class at or below it was removed; never decreases; advanced atomically with the removal) and `ReadGap` / `GapFrame` (the single informational `retention` frame a reader starting below a watermark gets before the data, with no cursor advance). `Report` carries the effective `PolicyID` (`WithPolicyID`), per-class `Watermark`, `Partial` (backlog/oldest unknown because the budget ran out), `Classified`, and `Changed()`; `Report.PassEvent()` returns the metadata-only `retention.pass` event (audit, private, actor janitor) only for a pass that changed something. A pass budget now caps every store call by what is left of `MaxRows` and `MaxElapsed` (also dry-run counts and the end-of-pass reads), checks it before the first call, and `Report.Elapsed` is returned. Rows on hold (pending outbox, unknown trusted kind, invalid metadata) are never classified, rolled up, tombstoned or trimmed. Stores never remove an R-audit row, even when a spec names it. A failed or timed-out end-of-pass read, and a dry-run count that stopped at its budget, mark the class and the report `Partial` and are counted as errors instead of reading as zero.
- `streamhub/retention/storetest`: the conformance suite for `Store` (`Run`) and `Mem`, an in-memory reference store in both trim modes. Cases for every `TrimPerRow` precondition (audit never removed, row not past every tier stays, no removal without a committed covering rollup, per-class cursor order), the persisted per-class watermark and its atomicity with the removal (fault injection via `Faulty`), rollup counts+flag atomicity and replay, the reader gap frame, rows on hold, cancelled contexts and `MaxDuration` (for stores that declare `TimeBounded`).

## v0.3.0 — 2026-10-16

### Added

- `streamhub/retention`: configuration and decision model for tiered event retention (design D7 rev 2). Per-class `full` / `envelope` ages and rollup schedules (zero = forever), `Defaults()` with the proposed tiers, `Config.Resolve()` merging over defaults with validation (lengthening the pii payload cap or shortening the audit class below 400d needs `acknowledge` and yields a warning; storing secret payloads is always rejected), and `Policy.ClassOf` / `PayloadMax` / `TierAt` for a janitor or log to ask which tier an event of a given age is in. No storage code.
- `streamhub/logkit`: shared pieces of a durable `streamhub.Log`, so backends agree on identity and deduplication: the opaque cursor `<epoch>:<seq>` (`FormatCursor`, `ParseCursor`, `ErrBadCursor`), the duplicate-detection `Digest` over Name and a canonical form of JSON Data (sorted keys, compact, number literals kept; non-JSON byte-exact), `Columns` / `Extractor` / `EnvelopeExtractor` for the indexed scope columns, and the external high-water `Marker` (`ReadMarker`, `WriteMarker`, `Marker.Behind`, `ErrMarkerUnreadable`) that detects a rolled-back or replaced store.

## v0.2.0 — 2026-10-16

### Added

- `streamhub`: `Event.ID`, an optional idempotency key. Appending the same ID with the same content returns the stored record and consumes no cursor; different content fails with `ErrEventIDConflict`. `Record.ID` echoes it.
- `streamhub`: `Head.Epoch`, `Record.Epoch` and `Gap.Epoch`: the identity of the log that assigned the cursors (not a producer restart counter), the optional `EpochReader` interface, and `GapEpochChanged` for a reader whose remembered epoch is not the log's. `MemoryLog` implements all of it; `hubtest.Conformance` covers it.

## v0.1.0 — 2026-10-03

- Module path is now `github.com/hollis-labs/libs/ui/ui-go` (was `github.com/hollis-labs/libs/ui-go`); the previous path and its `ui-go/vX.Y.Z` tags no longer exist. Tags are `ui/ui-go/vX.Y.Z`. No code change besides the path.
First release of the ui-go module: the packages of six former Hollis Labs modules, moved in with their git history.

### Added

- `ui-go/ssekit`: A Server-Sent Events writer, serve loop, resumable client and test harness that never look inside the payload (from `go-ssekit`, 10 commits of history).
- `ui-go/streamhub`: A payload-opaque, stdlib-only replayable fan-out hub with per-stream cursors, gap signals, a replay ring and slow-consumer policies (from `go-streamhub`, 12 commits of history).
- `ui-go/chatstream`: One canonical chat-stream event vocabulary with per-dialect decoders, per-target encoders, a reducer and a conformance kit (from `go-chatstream`, 11 commits of history).
- `ui-go/webui`: A small, dependency-free harness for serving a compiled single-page application out of any fs.FS, with the routing rules a SPA needs (from `go-webui`, 8 commits of history).
- `ui-go/directives`: A pure-Go parser for chat directives, the inline :: commands embedded in conversation text (from `go-directives`, 10 commits of history).
- `ui-go/envelopes`: The shared Go primitives for the Envelope UI Protocol, a wire format for typed, host-rendered payloads, with code generation and conformance tests (from `go-envelopes`, 42 commits of history).

Each lib keeps its own `CHANGELOG.md` (the history of the old module, as written) and has a `MIGRATION.md` with the old and new import paths.

### Changed

- The old modules' release tags were not carried over; this is the first release of the module.
- Import paths in code, documentation and tests moved to `github.com/hollis-labs/libs/ui/ui-go/...`, mechanically; no symbol was renamed or changed.
- go-chatstream required go-ssekit and go-streamhub as separate modules; in this module those are ordinary imports of `ui-go/ssekit` and `ui-go/streamhub`.
- `envelopes.ModulePath` is now `github.com/hollis-labs/libs/ui/ui-go`, the module that owns the package, not the package's import path (it was `github.com/hollis-labs/go-envelopes`). The catalog's source identity and the module line of generated TypeScript report the new value, and the "Code generated by" header of generated TypeScript names `github.com/hollis-labs/libs/ui/ui-go/envelopes/codegen`. Regenerating output with this version changes those lines.
