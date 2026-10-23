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
| `?` placeholders only | The store rebinds to `$1…` for Postgres with a tokenizer that skips string literals (including `E'…'` with backslash escapes), quoted identifiers, line and nested block comments, and `$tag$…$tag$` bodies (a `$` inside an identifier such as `a$b$c` is not a quote). The same tokenizer runs for SQLite. `??` is a literal `?` on both engines, outside literals and comments. |
| Never the JSONB operators `?`, `?\|`, `?&` | They collide with the placeholder; write `??\|` if you must, or use the `Dialect` JSON helpers. The lint flags `?\|` and `?&`. |
| `INSERT … ON CONFLICT (…) DO UPDATE / DO NOTHING`; never `INSERT OR …` / `REPLACE INTO` | Same syntax on both. |
| `RETURNING` for generated ids; never `LastInsertId` | Postgres has no `LastInsertId`. |
| No `AUTOINCREMENT` / `serial` in shared SQL | Per-dialect DDL belongs in the per-dialect migration files; prefer Go-generated ids. |
| Time comes from Go as a parameter; never `datetime('now')`, `now()`, `CURRENT_TIMESTAMP` | See Time. |
| Write `NULLS FIRST` / `NULLS LAST` explicitly | SQLite sorts NULLs smallest, Postgres largest. |
| `lower(col) LIKE lower(?) ESCAPE '\'` (`store.LikeInsensitive`, `store.LikeEscape`) | `LIKE` is case-insensitive on SQLite and case-sensitive on Postgres. SQLite's `lower()` folds ASCII only; for other scripts use `Dialect.Lower(expr)` (`gostore_lower`, a full-Unicode function the store registers for text; BLOBs, numbers and NULL pass through unchanged; Postgres `lower()` follows the database locale, so use a UTF-8 database). |
| `LIMIT n OFFSET m`, not `LIMIT m, n`; no `group_concat` | SQLite-only forms. |
| Booleans are Go `bool` | |
| JSON: use the `Dialect` helpers | `json_extract` vs `#>>`; numbers need explicit casts. |
| No `PRAGMA`, `GLOB`, `IFNULL`, `ILIKE` in shared SQL | Engine specific (use `COALESCE`). |

### Time

Time always comes from Go. `time.Time`, `*time.Time` and any `driver.Valuer` that yields a `time.Time`
(`sql.NullTime`) are encoded for you:

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
- a separate **read pool** (default 4 connections, WAL, opened `mode=ro`) for concurrent reads. A
  write through `Reader()` or `QueryContext` fails instead of taking the write lock.

`WriteTx` retries the whole transaction on `SQLITE_BUSY`/`LOCKED` (Postgres: serialization failure,
deadlock), so the callback must be safe to run again from scratch.

> **Nothing inside a `WriteTx` callback may use another connection.** Use only the `Tx` you were
> given. Calling `s.QueryContext` (the read pool) or `s.ExecContext` (the write connection, already
> held by your transaction) from inside the callback deadlocks or waits on a lock the transaction
> holds. Tether hit exactly this with `SetMaxOpenConns(1)`.

A callback that ends its goroutine (`runtime.Goexit`) is rolled back and releases the writer. `synchronous` defaults to `FULL`; set
`SQLiteConfig.Synchronous` (`NORMAL`, …) knowingly. `:memory:` and `file::memory:…` use one
connection for reads and writes (each connection to an in-memory URI would otherwise be its own
database). URIs with `mode=memory` (for example `file:name?mode=memory&cache=shared`) are refused at `Open`:
`core/util`'s sqlitekit would create real files with that name. Use `:memory:` or a file. For heavy write fan-in, `core/util/sqlite/serialwrite` can
batch writes in front of `Store.DB()`.

Backups: `Store.Backup(ctx, dest, store.BackupOptions{})` on SQLite (verified copy and manifest via
`core/util/sqlitebackup`, which is wrapped: `BackupOptions`/`BackupResult` are this package's own
types); `store.VerifyBackup`. Postgres is backed up out of process (`pg_dump` by ops) and `Backup`
returns `ErrBackupUnsupported`.

## Postgres

- **TLS**: `Open` refuses a connection that is not `sslmode=verify-full` on **every** host of the DSN
  (fallbacks included) unless `PostgresConfig.AllowInsecure` is set (local containers, CI). A
  Unix-domain socket host needs no TLS.
- **Transactions**: `WriteTx` is `READ COMMITTED` with retry on serialization failure and deadlock;
  `ReadTx` is `REPEATABLE READ`, read-only, so several queries see one snapshot (on SQLite a WAL
  read snapshot).
- **Credentials** (ADR `adr_shared_postgres_app_agent_access`): the app runs with a **DML-only**
  `DSN`; schema changes use a separate `MigrateDSN` (the schema owner). `migrate.Up` uses the
  migration credential, `migrate.Verify` uses the runtime one.
- Errors that could echo a DSN are not passed through; a parse failure never includes the DSN.
- `Config`, `PostgresConfig` and their `slog` values print `<redacted>` for both DSNs (and `json.Marshal` omits them), and an open
  `Store` keeps neither (`Store.Config()` returns them blank). Pass the `Config` you opened with to
  `migrate.Ensure`/`migrate.Up` when you need the migration credential.

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
err = migrate.Ensure(ctx, cfg, s, src) // cfg.SchemaMode auto: migrate, then verify. verify: verify only.
```

- `Config.SchemaMode` is `auto` or `verify`. Default: `auto` on SQLite, `verify` on Postgres.
- `Verify` reads the applied versions from the version table and needs **no DDL rights**. It compares
  the applied *set* with the build's versions, so it fails with `ErrSchemaMismatch` when the
  database is behind, ahead, or missing a version in the middle (0001 and 0003 applied, 0002 added
  later).
- Concurrent migrators on Postgres are serialised by goose's advisory lock (one lock id per version
  table), so racing starters queue and the later ones find nothing to do. **SQLite has no such
  lock:** two processes migrating one file can race, and the loser fails to start (busy or
  already-exists) without corrupting anything; a later start completes. Run the migrator from one
  process, or retry the start.
- `Source.Table` is spliced into SQL and must be a plain, unqualified identifier (no `schema.`; choose
the schema with `search_path`).
- `Baseline(ctx, s, src, version)` adopts a database whose schema already exists at `version` (for
  example from a custom version table): it records migrations up to `version` as applied without
  running them. `version` must be one of the source's versions. It refuses (`ErrAlreadyApplied`)
  when the table already records migrations; the check and the writes share one transaction (and,
  on Postgres, goose's advisory lock), so two concurrent baselines cannot both succeed. You must
  have checked that the live schema matches. Run `Up` afterwards for anything newer.
- Statements containing semicolons (triggers, functions) need goose `StatementBegin/End`.
- Go-coded migrations (`goose.WithGoMigrations`) are not wired in v1; add them when needed. goose
  is pinned to the newest release that builds with the pgx pin; `go mod tidy` and `govulncheck` run
  in this module's CI.

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
`AFTER INSERT/DELETE/UPDATE` triggers (the update trigger fires on any update but acts only when the
rowid or an indexed column changed, so changing an `INTEGER PRIMARY KEY` is safe; omit it with
`ImmutableContent`), tokenizer
configurable (default `unicode61 remove_diacritics 2`), optional prefix indexes. Postgres: a stored
generated `<Name>_vec` column with `setweight` per field, a GIN index, an `unaccent` wrapper for
diacritics, and `pg_trgm` indexes when `Fuzzy` is set. The extensions are created `SCHEMA
<ExtensionSchema>` (default `public`; that schema must exist and the runtime role must be able to
read it), and the migration role needs `CREATE` there (both extensions are "trusted" on Postgres 13+,
so a database owner can create them). `<` and `>` are turned into spaces before indexing and in
queries, because the default parser would otherwise read `<script>` as a tag and drop it. Table,
column and index names must be plain identifiers and not reserved words (`order`, `group`, …);
`Validate` refuses them. `gostore_unaccent` is shared by every index and `Down` does not drop it.
Adding the generated column rewrites the table under an `ACCESS EXCLUSIVE` lock: run that migration
in a quiet moment on a big table. 1–4 fields (Postgres has four weight
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

**Maintenance.** Both take a `store.Tx`, so call them inside `Store.WriteTx` (the FTS5 commands are
writes). `Rebuild` runs FTS5 `rebuild` after bulk loads that bypassed triggers or a `VACUUM`. SQLite
documents that `VACUUM` may renumber the rowids of a table without an `INTEGER PRIMARY KEY`; we could
not make it happen in testing, and `Rebuild` is the remedy if it does. On Postgres `Rebuild` does
nothing (the generated column is always current); a damaged GIN index needs `REINDEX`, which
requires table ownership, so run it with the migration credential. `Check` runs the FTS5
integrity check; on Postgres it confirms the GIN index exists and is valid and no row lacks a
vector (it cannot detect drift, because there is none). `Search` takes any `Querier`, a `Tx`
included, so a transaction sees its own writes.

**What differs between engines (accepted and documented).**

- **Scores are comparable within one dialect only** (`-bm25` vs `ts_rank_cd`); ranking order differs.
  Never compare scores across engines or assert absolute scores.
- Tokenisation differs (unicode61 vs the `simple` configuration): hyphenated words, URLs, e-mail
  addresses and numbers with separators split differently. No stemming by default on either;
  `Index.Language` (Postgres only) turns it on there.
- **Accent folding:** common accents fold the same (`café`/`cafe`, `résumé`/`resume`). Postgres
  `unaccent` also expands ligatures and special letters (`ß`→`ss`, `æ`→`ae`, `ø`→`o`, `œ`→`oe`);
  SQLite's `remove_diacritics 2` does not, so `strasse` finds `Straße` on Postgres only. A test
  pins this down (`TestKnownEngineDifferences`).
- `Substring` is case-insensitive for all of Unicode on SQLite (`gostore_lower`); on Postgres it
  follows the database locale (`lower()`), so use a UTF-8 database.
- Snippets pick different fragments. On Postgres the excerpt is taken from the folded text with
  `<`/`>` as spaces, so accents and angle brackets in it differ from the stored text. The excerpt is
  **HTML-escaped** before the markers go in (the database marks matches with per-query random
  stand-ins, so stored text can never forge or unbalance a marker) (`Snippet.NoEscape` for plain text); the markers
  themselves are inserted verbatim, so they are markup you choose, not user input. Ellipsis is
  SQLite only.
- Field-qualified phrases (`title:"brown fox"`) parse as the word `title:` plus a phrase; for a
  phrase restricted to a field build the `Expr` (`Term{Text: "brown fox", Field: "title"}`).
- A multi-word term with `Prefix` set prefixes only its last word on both engines.
- The result **sets** for words, phrases, prefixes, AND/OR/NOT and field queries are the same except
  for the cases above; the conformance suite asserts sets and top-k, not order.

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
// 2. in a drainer; the name is only a label, ownership is a per-claim token
host, _ := os.Hostname()
n, err := o.Drain(ctx, s, host, 20, func(ctx context.Context, m outbox.Message) error { return deliver(ctx, m) })
```

At-least-once delivery. A claim is a lease; a dead drainer's messages come back after `Lease`.
Every claim writes a random token, and `Ack`/`Fail` must present it while the lease is still running:
a worker whose lease ended gets `ErrLeaseLost` instead of clearing the new owner's claim, even if
both use the same worker name. `Drain` skips a message whose lease ran out before its turn and
gives the handler a context that ends with the lease; keep *batch size × worst-case handler time*
below `Lease`. Lease expiry is judged by each drainer's own clock, so drainers on different hosts
need clocks well within the lease. A handler should be idempotent (that is what at-least-once
means). Failures back off (`Backoff`) and a message that reaches `MaxAttempts` is parked; `Parked`
lists them and `Retry(id)` puts one back in line. `Pending` does not count parked messages. Error
text is stored cut to 500 bytes on a character boundary, with invalid UTF-8 and NUL removed. Handler panics
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
dropped afterwards, and a failed drop fails the test). The harness creates the `pg_trgm` and
`unaccent` extensions up front as the test user, so a test that needs the real migration role
(`search`'s `TestIndexMigratesUnderTheRealMigrationRole`) makes its own database and role. Without the
DSN the Postgres subtests **skip locally and fail** when `GOSTORE_REQUIRE_PG=1` or `CI` is set to
anything but empty, `0` or `false`. A throwaway
server:

```sh
docker run -d --name gostore-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
export GOSTORE_TEST_PG_DSN="host=127.0.0.1 port=55432 user=postgres password=test dbname=postgres sslmode=disable"
```

## Lint

```sh
go run github.com/hollis-labs/libs/core/store/cmd/gostore-lint ./...   # exit 1 on findings; a trailing /... is accepted
```

or in a test: `lint.Dir(".", lint.Options{SkipTests: true})`. Rules: `insert-or`, `sqlite-time`,
`db-clock`, `autoincrement`, `ilike`, `json-path`, `dollar-placeholder`, `sqlite-only`, `like-case`,
`last-insert-id`, `limit-comma`, `jsonb-operator`. SQL built by concatenation is folded and checked as one statement
(`"SELECT … " + "FROM …"`, same-file string constants, and `q += "…"` / `q = q + "…"` on a variable
that started as SQL); an operand built at run time is unknown, and a `LIKE` next to one is not
flagged. A waiver on a constant's declaration follows it to every use. Variables are tracked per
function. The lint still cannot see SQL assembled across functions, files or `fmt.Sprintf`. Block
comments nest in the tokenizer as on Postgres; SQLite does not nest them, so the lint can
under-scan SQL after an inner `*/` (SQLite-only quirk). Waive a deliberate exception with `// gostore:allow <rule>` on the line or the
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

## API shape

`Dialect` and `Tx` are sealed: only this package implements them, so adding a method is not a
breaking change. `Querier` is open; implement it for a test double or an instrumented wrapper (its
`Dialect()` must return a value obtained from `store.DialectFor` or a `Store`). Wrapped types
(`BackupOptions`, `BackupResult`) are this module's own; nothing from `core/util` appears in its
API.

## Tested, and not

Covered on both engines by the conformance suite: rebinding and `??`, time round trip and order
(including `sql.NullTime`), transactions and panic containment, `Goexit` in a write transaction,
`ReadTx` snapshot, migrations (verify with a gap, baseline checks and concurrent baselines,
concurrent starters, DML-only role), search sets, snippets, key updates, tag-shaped text, `VACUUM`
plus `Rebuild`, outbox leases, stale `Fail`, parked messages, error text. Not covered: Postgres
under a non-UTF-8 database locale, `Language` values other than `simple`/`english`, hosts with
skewed clocks, and fuzzing beyond the seeded corpus in CI (the fuzz targets run for a while locally).

## Not in v1

KV store, query builder/ORM, vector/embedding search, a Postgres driver for `core/util/queue`,
Go-coded migrations, own-content FTS tables.
