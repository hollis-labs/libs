# Changelog

All notable changes to the `streamlog-postgres` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`ui/streamlog-postgres/vX.Y.Z`.

## [Unreleased]

### Added

- `Log.RetentionStore()`: the log as a `retention.Store` for the retention janitor: classify, roll up closed buckets (flag per row), tombstone payloads (set-based `UPDATE`, `payload_digest` `sha256:…` or `withheld`), head trim (advances `pruned`, only rolled-up rows, R-audit never), prune rollups, in short transactions; dry-run counts bounded by `Op.MaxDuration`; the janitor lock is a PostgreSQL advisory lock shared by all instances. Passes `retention/storetest`.
- `Options.Retention` / `RetentionPolicyID`: classify at append and stamp the admitted policy id (audit only; D7 rev 2.2).
- Schema: `class`, `policy_id`, `rolled`, `outcome` on records; rollups keyed by class and bucket size.

### Added

- `Open` / `OpenDSN`: a durable `streamhub.Log` on PostgreSQL, the target store of the platform event log, with the SQLite log's contract and columns: gap-free cursors per stream (several writers allowed), log epoch, computed gaps, `Event.ID` idempotency (same content returns the original record, different content is `ErrEventIDConflict`, counted and hooked), indexed scope columns, JSONB payload copy, tier and `payload_digest` columns, rollup table. Passes `hubtest.Conformance` and `hubtest.HubSuite`.
- Restore detection: the server's system identifier and timeline id stored in `log_meta` (cluster swap, point-in-time recovery, promotion) and an external high-water marker on the writer's host, on by default (pg_restore into the same cluster, snapshot rollback). Any mismatch re-mints the epoch, or fails `Open` under `RefuseOnMismatch`; disabling the marker warns.
- Tests need a server: set `STREAMLOG_PG_DSN`; they skip otherwise.
