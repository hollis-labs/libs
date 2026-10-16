# Changelog

All notable changes to the `streamlog-postgres` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`ui/streamlog-postgres/vX.Y.Z`.

## [Unreleased]

### Added

- `Open` / `OpenDSN`: a durable `streamhub.Log` on PostgreSQL, the target store of the platform event log, with the SQLite log's contract and columns: gap-free cursors per stream (several writers allowed), log epoch, computed gaps, `Event.ID` idempotency (same content returns the original record, different content is `ErrEventIDConflict`, counted and hooked), indexed scope columns, JSONB payload copy, tier and `payload_digest` columns, rollup table. Passes `hubtest.Conformance` and `hubtest.HubSuite`.
- Restore detection: the server's system identifier and timeline id stored in `log_meta` (cluster swap, point-in-time recovery, promotion) and an external high-water marker on the writer's host, on by default (pg_restore into the same cluster, snapshot rollback). Any mismatch re-mints the epoch, or fails `Open` under `RefuseOnMismatch`; disabling the marker warns.
- Tests need a server: set `STREAMLOG_PG_DSN`; they skip otherwise.
