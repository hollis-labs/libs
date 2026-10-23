# store

One set of handwritten SQL, two engines. `core/store` lets an app keep its own SQL and run it on
**SQLite** (tests, a single box, local spools and caches) or **Postgres** (a shared server) by
switching a driver in configuration. It is an adapter layer, not an ORM: no query builder, no
codegen, no models. Your narrow store interfaces stay yours; you add a Postgres implementation
table by table.

```sh
go get github.com/hollis-labs/libs/core/store
```

| Package | What it is |
|---|---|
| `store` | `Open`, `Config`, `Store`, `Dialect`, transactions, time codec, `Rebind`, backup |
| `store/migrate` | goose v3 per-dialect migrations, parity check, `Verify`, `Ensure`, `Baseline` |
| `store/search` | portable full-text search: FTS5 and tsvector/pg_trgm behind one query model |
| `store/outbox` | transactional outbox with leased claims |
| `store/lint`, `cmd/gostore-lint` | the portable-SQL rules as a check |
| `store/storetest` | run a test on both engines |

## Quick start

```go
s, err := store.Open(ctx, store.Config{
    Driver: store.DriverSQLite,
    SQLite: store.SQLiteConfig{Path: "app.db", CreateParentDir: true},
})
// or
s, err := store.Open(ctx, store.Config{
    Driver:   store.DriverPostgres,
    Postgres: store.PostgresConfig{DSN: os.Getenv("APP_DB_DSN"), MigrateDSN: os.Getenv("APP_DB_MIGRATE_DSN")},
})
defer s.Close()

err = s.WriteTx(ctx, func(ctx context.Context, tx store.Tx) error {
    var id int64
    if err := tx.QueryRowContext(ctx,
        `INSERT INTO jobs (name, created_at) VALUES (?, ?) RETURNING id`, name, time.Now()).Scan(&id); err != nil {
        return err
    }
    _, err := o.Enqueue(ctx, tx, "job.created", payload) // same transaction
    return err
})

rows, err := s.QueryContext(ctx, `SELECT id, name FROM jobs WHERE lower(name) LIKE lower(?) ESCAPE '\'`, pat)
```

`?` placeholders and `time.Time` arguments are handled for you on every call.

## Portable SQL rules

Shared SQL (the SQL strings in your Go code) follows these rules. `gostore-lint` enforces them.

| Rule | Why |
|---|---|
| `?` placeholders only | The store rebinds to `$1…` for Postgres with a tokenizer that skips string literals, quoted identifiers, comments and dollar-quoted bodies. `??` is a literal `?`. |
| `INSERT … ON CONFLICT (…) DO UPDATE / DO NOTHING`; never `INSERT OR …` / `REPLACE INTO` | Same syntax on both. |
| `RETURNING` for generated ids; never `LastInsertId` | Postgres has no `LastInsertId`. |
| No `AUTOINCREMENT` / `serial` in shared SQL | Per-dialect DDL belongs in the per-dialect migration files; prefer Go-generated ids. |
| Time comes from Go as a parameter; never `datetime('now')`, `now()`, `CURRENT_TIMESTAMP` | See Time. |
| Write `NULLS FIRST` / `NULLS LAST` explicitly | SQLite sorts NULLs smallest, Postgres largest. |
| `lower(col) LIKE lower(?) ESCAPE '\'` (`store.LikeInsensitive`, `store.LikeEscape`) | `LIKE` is case-insensitive on SQLite and case-sensitive on Postgres. |
| Booleans are Go `bool` | |
| JSON: use the `Dialect` helpers | `json_extract` vs `#>>`; numbers need explicit casts. |
| No `PRAGMA`, `GLOB`, `IFNULL`, `ILIKE` in shared SQL | Engine specific (use `COALESCE`). |

### Time

Time always comes from Go. `time.Time` and `*time.Time` arguments are encoded for you:

- **SQLite** stores fixed-width UTC text `2006-01-02T15:04:05.000000000Z` (`store.TimeFormat`). It is
  *not* RFC 3339Nano, which trims trailing zeros and stops sorting in time order.
- **Postgres** stores `timestamptz`.
- Both are truncated to microseconds in Go (`store.Canonical`), so a value reads back identically on
  either engine.

Read with `row.Scan(store.ScanTime(&t))` or `store.ScanNullTime(&p)`. Declare the column `TEXT` in
the SQLite migration and `TIMESTAMPTZ` in the Postgres one.

### JSON

```go
d := s.Dialect()
name, _ := d.JSONText("doc", "user", "name") // json_extract(...) / (doc)::jsonb #>> '{user,name}'
n, _ := d.JSONInt("doc", "stats", "count")   // CAST(... AS INTEGER) / ...::bigint
q := `SELECT id FROM events WHERE ` + n + ` > ?`
```

Columns are `TEXT` on SQLite and `jsonb` on Postgres. Column and path segments are validated as plain
identifiers because they are spliced into SQL. Array indexes are numeric segments (`"list", "1"`).
`JSONText` is for string values; use `JSONInt/Float/Bool` for the rest.

## SQLite: the single-writer model

SQLite has one writer. `Open` gives you:

- a **write pool of one connection** with `BEGIN IMMEDIATE` transactions (`core/util/sqlite`:
  `sqlitekit`, `txutil`), and
- a separate **read pool** (default 4 connections, WAL) for concurrent reads.

`WriteTx` retries the whole transaction on `SQLITE_BUSY`/`LOCKED` (Postgres: serialization failure,
deadlock), so the callback must be safe to run again from scratch.

> **Nothing inside a `WriteTx` callback may use another connection.** Use only the `Tx` you were
> given. Calling `s.QueryContext` (the read pool) or `s.ExecContext` (the write connection, already
> held by your transaction) from inside the callback deadlocks or waits on a lock the transaction
> holds. Tether hit exactly this with `SetMaxOpenConns(1)`.

`synchronous` defaults to `FULL`; set `SQLiteConfig.Synchronous` (`NORMAL`, …) knowingly. `:memory:`
uses one connection for reads and writes. For heavy write fan-in, `core/util/sqlite/serialwrite` can
batch writes in front of `Store.DB()`.

Backups: `Store.Backup(ctx, dest)` on SQLite (verified copy via `core/util/sqlitebackup`);
`store.VerifyBackup`. Postgres is backed up out of process (`pg_dump` by ops).

## Postgres

- **TLS**: `Open` refuses a connection that is not `sslmode=verify-full` unless
  `PostgresConfig.AllowInsecure` is set (local containers, CI).
- **Credentials** (ADR `adr_shared_postgres_app_agent_access`): the app runs with a **DML-only**
  `DSN`; schema changes use a separate `MigrateDSN` (the schema owner). `migrate.Up` uses the
  migration credential, `migrate.Verify` uses the runtime one.
- Errors that could echo a DSN are not passed through; a parse failure never includes the DSN.

## Migrations (`store/migrate`)

Built on **goose v3** (provider API). Layout:

```
migrations/sqlite/0001_init.sql
migrations/postgres/0001_init.sql
```

```go
//go:embed migrations
var fs embed.FS
var src = migrate.Source{FS: fs, Root: "migrations", Table: "app_goose_version"}

func TestParity(t *testing.T) { // identical version numbers per dialect
    if err := migrate.CheckParity(src); err != nil { t.Fatal(err) }
}

// at startup
err = migrate.Ensure(ctx, s, src) // SchemaMode auto: migrate, then verify. verify: verify only.
```

- `Config.SchemaMode` is `auto` or `verify`. Default: `auto` on SQLite, `verify` on Postgres.
- `Verify` reads one row from the version table and needs **no DDL rights**; it fails with
  `ErrSchemaMismatch` when the database is behind or ahead of the build.
- Concurrent migrators on Postgres are serialised by goose's advisory lock (one lock id per version
  table). SQLite needs no lock.
- `Baseline(ctx, s, src, version)` adopts a database whose schema already exists at `version` (for
  example from a custom version table): it records migrations up to `version` as applied without
  running them. It refuses when the table already records migrations. You must have checked that
  the live schema matches.
- Statements containing semicolons (triggers, functions) need goose `StatementBegin/End`.
- Go-coded migrations (`goose.WithGoMigrations`) are not wired in v1; add them when needed.

## Full-text search (`store/search`)

One index definition and one query model; FTS5 on SQLite, a generated `tsvector` column on Postgres.

```go
var notes = search.Index{
    Name: "notes_fts", Table: "notes", IDColumn: "id",
    Fields:  []search.Field{{Column: "title", Weight: 5}, {Column: "body", Weight: 1}},
    Filters: []string{"kind"},
}

// Migration: emit the DDL once per dialect.
sql, _ := notes.GooseSQL(dialect) // paste into migrations/<dialect>/000N_notes_fts.sql

sr, _ := search.New(s.Dialect(), notes)
hits, err := sr.Search(ctx, s.Reader(), search.Query{
    Text:    `lazy "brown fox" -cat title:quick`,
    Filters: []search.Filter{{Column: "kind", Values: []any{"a"}}},
    Limit:   20,
    Snippet: &search.Snippet{Field: "body"},
})
// hits[i].ID (text), .Score (higher is better), .Snippet
```

**Index.** The text lives in the source table (external content). SQLite: an FTS5 table plus
`AFTER INSERT/DELETE/UPDATE` triggers (omit the update trigger with `ImmutableContent`), tokenizer
configurable (default `unicode61 remove_diacritics 2`), optional prefix indexes. Postgres: a stored
generated `<Name>_vec` column with `setweight` per field, a GIN index, an `unaccent` wrapper for
diacritics, and `pg_trgm` indexes when `Fuzzy` is set. 1–4 fields (Postgres has four weight
classes); weights map to classes by rank. `Up` also backfills existing rows.

**Query model.** `Expr` is `Term{Text, Prefix, Phrase, Field}`, `And`, `Or`, `Not`. `search.Parse`
turns user text into one: words, `"phrases"`, `prefix*`, `AND` / `OR` / `NOT` (upper case),
`-word`, `(groups)`, `field:term`; `ParseOptions{Any: true}` makes adjacent words OR (recall mode),
`PrefixLast` is typeahead, `NoOperators` treats everything as text. `Parse` never fails: stray or
doubled operators become ordinary terms and punctuation-only input matches nothing. **Raw user
text never reaches `MATCH` or `to_tsquery`**; every term is quoted and escaped for the engine. A
hand-built `Expr` that is structurally invalid (standalone `Not`, unknown field) is an
`ErrInvalidQuery`. `Not` is a filter: it needs a positive sibling in an `And`.

**Results.** `Hit{ID, Score, Snippet}`. Order is score descending, then id, so paging is stable.
`Limit` defaults to 20 (max 1000). `Query.Substring` is a case-insensitive substring search (LIKE
based; trigram-indexed on Postgres with `Fuzzy`), with a constant score.

**Maintenance.** `Rebuild` (FTS5 `rebuild`; Postgres `REINDEX`) after bulk loads that bypassed
triggers or a `VACUUM` that renumbered rowids (a SQLite table without an `INTEGER PRIMARY KEY` has
unstable rowids); `Check` runs the FTS5 integrity check (Postgres: no row lacks a vector). Both
accept a `Tx`, and `Search` accepts a `Tx` too, so a transaction sees its own writes.

**What differs between engines (accepted and documented).**

- **Scores are comparable within one dialect only** (`-bm25` vs `ts_rank_cd`); ranking order differs.
  Never compare scores across engines or assert absolute scores.
- Tokenisation differs (unicode61 vs the `simple` configuration): hyphenated words, URLs, e-mail
  addresses and numbers with separators split differently. No stemming by default on either.
- Snippets pick different fragments; on Postgres with diacritic folding the excerpt is the folded
  text. Snippets are not HTML-escaped.
- The result **sets** for words, phrases, prefixes, AND/OR/NOT and field queries are the same; the
  conformance suite asserts sets and top-k, not order.

Using it as the fallback when embeddings are absent needs nothing else: there is no vector
dependency. (Embeddings/vector search are out of scope.)

**Not yet:** FTS tables that hold their own content (no source table, as Fragments Engine's
`transcript_search`) and expression columns; the index covers source-table columns.

## Outbox (`store/outbox`)

```go
o, _ := outbox.New(outbox.Config{Lease: time.Minute})
// DDL for the migration, per dialect: o.DDL(dialect)

// 1. in the business transaction
_, err = o.Enqueue(ctx, tx, "invoice.paid", payload)
// 2. in a drainer
n, err := o.Drain(ctx, s, "worker-1", 20, func(ctx context.Context, m outbox.Message) error { return deliver(m) })
```

At-least-once delivery. A claim is a lease; a dead drainer's messages come back after `Lease`.
Failures back off (`Backoff`) and a message that reaches `MaxAttempts` is parked. Handler panics
are contained (`core/panics`) and recorded as a reference, not as the panic text. The claim
statement is per dialect (`ClaimSQL`): `FOR UPDATE SKIP LOCKED` on Postgres, none on SQLite where
writers are already serialised. `core/util/queue` has no Postgres driver; a queue driver over this
table is a separate piece of work.

## Panic handling

Transaction callbacks and outbox handlers run under `core/panics`. A panic rolls the transaction
back and returns a `*panics.Error` whose text is a reference only; the redacted detail goes to the
configured reporter. Set `Config.Guard` (or `panics.SetDefault` in `main`) to choose the reporter
and redaction.

## Testing: `store/storetest`

```go
func TestJobs(t *testing.T) {
    storetest.EachDialect(t, func(t *testing.T, s *store.Store) {
        // s is a fresh, empty database on each engine
    })
}
```

SQLite always runs. Postgres runs against `GOSTORE_TEST_PG_DSN` (each test gets a private schema,
dropped afterwards; the user must be able to `CREATE EXTENSION` for `search`). Without the DSN the
Postgres subtests **skip locally and fail** when `GOSTORE_REQUIRE_PG=1` or `CI` is set. A throwaway
server:

```sh
docker run -d --name gostore-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
export GOSTORE_TEST_PG_DSN="host=127.0.0.1 port=55432 user=postgres password=test dbname=postgres sslmode=disable"
```

## Lint

```sh
go run github.com/hollis-labs/libs/core/store/cmd/gostore-lint ./...   # exit 1 on findings
```

or in a test: `lint.Dir(".", lint.Options{SkipTests: true})`. Rules: `insert-or`, `sqlite-time`,
`db-clock`, `autoincrement`, `ilike`, `json-path`, `dollar-placeholder`, `sqlite-only`, `like-case`,
`last-insert-id`. Waive a deliberate exception with `// gostore:allow <rule>` on the line or the
line above. Migration `.sql` files are per dialect by design and are not linted.

## Adoption guide

1. **Inventory** the app's SQL strings. Run `gostore-lint` to see what is not portable.
2. **Wrap, don't rewrite.** Replace `*sql.DB` in the store type with `*store.Store`; use `tx`
   from `WriteTx` for writes and `s.QueryContext` for reads. Keep your interfaces.
3. **Fix findings** per the rules table (`INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`,
   `datetime('now')` → a Go `time.Time` parameter, `LastInsertId` → `RETURNING id`, …).
4. **Migrations**: move to `migrations/sqlite` and `migrations/postgres`; for an app with an
   existing version table, `migrate.Baseline` at the current version, then add new migrations to
   both directories. Add the parity test.
5. **Dual-engine tests**: wrap the app's store tests in `storetest.EachDialect`.
6. **Postgres is per app and per table**: ship the Postgres implementation only for the tables that
   need it; SQLite stays the default for tests, local spools and caches.
7. **Search**: define an `Index`, add `GooseSQL` output to both migration directories, and call
   `Searcher.Search` where the FTS5 query is today.

## Not in v1

KV store, query builder/ORM, vector/embedding search, a Postgres driver for `core/util/queue`,
Go-coded migrations, own-content FTS tables.
