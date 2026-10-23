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

### Changed in review, round 2 (PR 122)

- `outbox`: a message `Drain` skips because an earlier handler overran the lease gets its attempt
  back and its claim released; before, such messages were charged at claim, re-claimed and skipped
  round after round until parked without their handler running. A test fails without the refund.
- `search`: snippet stand-ins are random per query (stored text, including U+E000–E002, can no
  longer forge or unbalance a marker). A term that is only angle brackets is `ErrInvalidQuery` on
  SQLite as it already was on Postgres.
- `lint`: resolves same-file string constants, checks `q += "…"` and `q = q + "…"` on a variable that
  began as SQL, and no longer flags a `LIKE` beside a run-time operand. Known gap: SQL built across
  functions or with `fmt.Sprintf`.
- `migrate`: a schema-qualified `Source.Table` is refused (it broke `Baseline` on SQLite).
- `store`: a `driver.Valuer` is evaluated once; `Config`/`PostgresConfig` DSNs are `json:"-"`;
  `gostore_lower` leaves BLOBs unchanged.
- Known gaps recorded: `file:name?mode=memory&cache=shared` is read as a file path by
  `core/util` sqlitebackup/sqlitekit; SQLite does not nest `/* */` comments though the tokenizer does.

### Changed in review, round 3 (PR 122)

- `outbox`: when `Drain` skips a message after an overrun, the attempt refund is separate from the
  claim release, so it still lands when another drainer has already re-claimed the message (before,
  such a message was charged twice and parked early). A concurrent-drainer test asserts attempts
  equal handler calls; it fails on the previous behaviour.
- `search`: the snippet stand-ins are bound parameters, so every snippet search has the same
  statement text and the Postgres driver reuses one prepared statement (50 identical searches used
  to prepare 50). A test counts `pg_prepared_statements`.
- `lint`: `... LIKE " + pat` is flagged again when the column side has no `lower()`; an appended
  piece is checked together with the text before it (no false positive for `lower(a)` then
  `LIKE lower(?)`); a `gostore:allow` on a constant follows it to each use; `INSERT INTO " + t + "
  DEFAULT VALUES` is flagged; variables and local constants are scoped to their function.
- `store`: a `driver.Valuer` that fails returns its error from the statement (no second call);
  `mode=memory` file URIs are refused at `Open` instead of creating files.
- Known gaps: the lint resolves only same-file constants and in-function variables.

### Changed in review, round 4 (PR 122)

- `outbox`: the table has an `epoch` column that `Retry` bumps and `Claim` returns; the skip refund
  applies only within the claim's epoch, so a Retry in between cannot be undercounted. Known limit:
  a refund that lands after another drainer delivered the message leaves one unrefunded charge on a
  delivered row (metrics only).
- `lint`: findings are counted per rule, so a waiver on earlier text or on a constant covers only
  those matches and a new violation of the same rule in appended text is reported. Clause fragments
  (`WHERE …`, `AND …`, `SET …`, `ORDER BY …`, `JOIN …`, `ON …`, …) are linted as SQL, so
  `"WHERE name LIKE " + pat` flags. `INSERT INTO "t"` and `INSERT INTO s.t DEFAULT VALUES` flag.
- `store`: every `file:` path except `file::memory:` is refused at `Open` (case and encoding
  variants created real files). A failing `Valuer`'s error reads `store: argument N: …` on Postgres.
