# Changelog

All notable changes to go-context-window are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- Extracted from Nanite's `internal/context` package: the fixed 15-slot
  `SlotOrder`, per-slot `DefaultBudgets` and `DefaultCompactable`,
  `ContextWindow` (budget allocation, per-slot SHA-256 cache keys,
  `Assemble`), the four-stage `CompactionPipeline`, handoff payloads and
  the Glass-4 envelope, context-overflow classification and
  `ProviderSummarizer`. Slot names, order, default values and stage order
  are identical to the seed.
- `NamedStage`, exported (the seed's `DefaultStages` returned an
  unexported type).
- `INVARIANTS.md`: the mechanism-level guarantees; Nanite's own tests remain
  the full seven-invariant enforcement point.
- Tests: the ported seed suite, `TestSlotOrder_MatchesSeedSnapshot`,
  `TestDefaults_MatchSeedSnapshot`, `TestDefaults_CompleteForSlotOrder`,
  compile-time interface assertions, and examples for `NewContextWindow` and
  `CompactionPipeline.Run`.

### Changed

- Package renamed from `context` to `contextwindow` so it no longer
  shadows the standard library package.
- `CompactionEvent.ID` is now a random 128-bit hex string from
  `crypto/rand` instead of a `google/uuid` string; it is opaque and no
  longer RFC 4122 shaped. The `google/uuid` dependency is dropped.
- Doc comments no longer cross-reference Nanite-internal symbols and files.
