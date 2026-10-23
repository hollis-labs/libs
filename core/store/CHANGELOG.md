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

### Decisions recorded

- **D7 override.** The rulings (D1–D9) deferred full-text search to an interface; Chrispian
  overrode that, so portable full-text search ships in v1.
- **Dependencies.** pgx ≥ 5.9.2 (v5.11.0) for GO-2026-5004 / 4771 / 4772; goose v3.29.0 (the newest
  release builds with that pgx). The standing preference is the latest versions unless something
  concrete breaks. Go 1.26.10 (GO-2026-6809). `govulncheck` runs in this module's CI.
- Not wired: Go-coded goose migrations, own-content FTS tables, expression columns, a Postgres
  driver for `core/util/queue`.

### Changed in review (PR 122)

- `store`: `??` collapses to `?` on SQLite and Postgres alike, never inside a literal or comment.
  The tokenizer handles `E'…'` backslash escapes, nested block comments and `a$b$c`, and its masking
  covers dollar-quoted bodies (now `internal/sqlmask`; `store.Mask` is removed).
- `store`: Postgres `ReadTx` is `REPEATABLE READ`. `sql.NullTime` and other `driver.Valuer`s that
  yield a time are encoded like `time.Time`. `Config`/`PostgresConfig` redact DSNs in `%v`, `%+v`,
  `%#v` and `slog`, and an open `Store` keeps no connection string (`Config()` returns them blank).
  The rebind cache is bounded. `file::memory:` shares one connection. The SQLite read pool is
  `mode=ro`. A write transaction whose callback calls `runtime.Goexit` rolls back and frees the writer.
  The TLS check applies to every host of a multi-host DSN; a Unix socket needs none. SQLite case folding:
  `Dialect.Lower`, backed by a registered full-Unicode `gostore_lower`.
- `store` API shape: `Dialect` and `Tx` are sealed; `Backup` takes `BackupOptions` and returns
  `BackupResult` (no `core/util` types in the API); `OpenMigration` is documented correctly.
- `migrate`: `Verify` compares the applied set, not `max(version)`. `Baseline` requires a source
  version and does its check and writes in one transaction under goose's advisory lock on Postgres.
  Missing-table detection uses SQLSTATE 42P01 / "no such table". `Source.Table` is validated.
  `Ensure` now takes the `Config` (`Ensure(ctx, cfg, s, src)`) because the Store no longer holds the
  migration credential.
- `search`: the SQLite update trigger also fires for a change of the integer primary key. A prefixed
  multi-word term prefixes only its last word on Postgres, as on FTS5. Extensions are created
  `SCHEMA <ExtensionSchema>` (was `UnaccentSchema`) and the trigram operator class is qualified.
  Postgres `Rebuild` is a no-op (no `REINDEX` privilege needed) and `Check` verifies the index;
  both now take a `Tx`. Snippets are HTML-escaped before markers are added (`Snippet.NoEscape`
  opts out) and any marker text is accepted. `<`/`>` no longer swallow text on Postgres. Reserved
  words are refused as names. `Substring` folds Unicode case on SQLite. Documented engine
  differences: ligature folding, field-qualified phrases, Postgres excerpts.
- `outbox`: every claim carries a random token; `Ack`/`Fail` take the claimed `Message`, require the
  token and a running lease, and return `ErrLeaseLost` otherwise. `Drain` skips messages whose
  lease ended and bounds the handler context by the lease. Error text is cut on a character
  boundary and cleaned of invalid UTF-8 and NUL. Added `Parked` and `Retry`; `Pending` excludes
  parked messages; a nil payload is stored as empty; a schema-qualified table name is refused.
- `lint`: `gostore-lint ./...` works; concatenated string SQL is folded; new rules `jsonb-operator`
  and `limit-comma`, `group_concat` joins `sqlite-only`; `serial` only counts in `CREATE`/`ALTER`;
  `lower(coalesce(a, b)) LIKE lower(?)` no longer false-positives.
- `storetest`: `CI=false`/`0` no longer counts as set; a failed `DROP SCHEMA` fails the test.
