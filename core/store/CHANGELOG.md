# Changelog

This module is versioned independently with `core/store/vX.Y.Z` tags.

## Unreleased

### Added

- `store`: `Open`/`Config` for SQLite (single writer plus read pool, IMMEDIATE write transactions,
  `synchronous=FULL` by default) and Postgres (pgx v5 through `database/sql`, `verify-full` TLS
  unless `AllowInsecure`); `Store.WriteTx`/`ReadTx` with panic containment and lock/serialization
  retry; the `Dialect` adapter (`?` to `$n` rebinding with a tokenizer, time encoding, JSON path
  helpers, claim clause); fixed-width UTC time codec; `Store.Backup` for SQLite.
- `migrate`: goose v3 provider per dialect directory, dialect parity check, `Verify` without DDL
  rights, `Ensure` with `auto|verify` schema mode, separate migration credential, `Baseline`.
- `search`: portable full-text search (FTS5 and tsvector/pg_trgm), query model and parser,
  normalised scores, snippets, rebuild and integrity check, migration DDL generation.
- `outbox`: transactional outbox with leased claims (`FOR UPDATE SKIP LOCKED` on Postgres).
- `lint` and `cmd/gostore-lint`: the portable-SQL rules as a check over SQL strings and Go source.
- `storetest`: run a test body on SQLite and Postgres (`GOSTORE_TEST_PG_DSN`; fails instead of
  skipping when `GOSTORE_REQUIRE_PG=1` or `CI` is set).
