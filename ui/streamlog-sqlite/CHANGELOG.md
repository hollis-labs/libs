# Changelog

All notable changes to the `streamlog-sqlite` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`ui/streamlog-sqlite/vX.Y.Z`.

## [Unreleased]

### Added

- `Log.RetentionStore()`: the log as a `retention.Store`, so a `retention.Janitor` can classify, roll up (counts per bucket with first and last cursor, flagged per row), tombstone payloads (`tier=1`, `payload_digest` `sha256:…` or `withheld`), trim from the head (advancing `pruned_through` like `Log.Trim`, only rolled-up rows, R-audit never) and prune rollups, in short bounded transactions. Passes `retention/storetest`.
- `Options.Retention` / `RetentionPolicyID`: records are classified at append and stamped with the policy they were admitted under (audit only; the janitor applies the current configuration to every row, D7 rev 2.2).
- Schema: `class`, `policy_id`, `rolled` and `outcome` columns on records; rollups keyed by class and bucket size.

### Changed

- The cursor encoding, the duplicate-detection digest, the envelope extractor and the restore marker now come from `ui-go/streamhub/logkit` (shared with the Postgres log) instead of private copies. `FormatCursor`, `ParseCursor`, `ErrBadCursor`, `Columns`, `Extractor` and `EnvelopeExtractor` moved there; use `logkit.*`. Requires `ui/ui-go` v0.3.0.

### Added

- `Open`: a durable `streamhub.Log` on SQLite (single writer connection, separate read pool, WAL), passing `hubtest.Conformance` and `hubtest.HubSuite`.
- Log epoch: minted at database creation, kept across restarts, re-minted when the database is recreated or restored. Opaque cursor `<epoch>:<seq>` (`FormatCursor`, `ParseCursor`, `Log.AfterCursor`); a stale epoch gets a `GapEpochChanged` gap.
- Restore detection: a high-water marker file outside the database (`<db>.hwm`). `Options.OnStorageMismatch` chooses between re-minting the epoch (default) and failing `Open` with `ErrStorageMismatch`; `Log.Recovery` says what happened.
- Computed gaps from the pruned-through cursor and the epoch; every gap carries reason, earliest available, high water and epoch.
- Idempotent appends on `Event.ID`: the same content returns the original record, different content returns `ErrEventIDConflict`, is counted (`Log.Stats`) and reported through `Options.OnIDConflict`.
- Indexed scope columns lifted by an `Extractor` (default `EnvelopeExtractor`), the tier and `payload_digest` columns for retention, and the rollup table. No retention janitor yet (CW-20261016-0146).
