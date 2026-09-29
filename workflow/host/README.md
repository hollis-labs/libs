# go-workflow-host

Host-side companions for [go-workflow](https://github.com/hollis-labs/go-workflow):
building blocks a host application needs to run the engine, kept out of
go-workflow's core so the core stays free of persistence and I/O
dependencies. The module has no root package; import the packages below.
Every package is standard library plus go-workflow only.

| Package | What it gives you |
|---|---|
| [`sqlstore`](#sqlstore) | a SQLite `runtime.StateStore` (and every companion runtime store interface) |
| [`artifactfs`](#artifactfs) | a local-filesystem `values.ArtifactStore` |

## Install

```sh
go get github.com/hollis-labs/go-workflow-host/sqlstore
```

## sqlstore

`sqlstore.Store` implements `runtime.StateStore` and the other 20 runtime store
interfaces (waits, memoization, recovery, compensation, scheduler resources,
and the rest) over a `*sql.DB`. It is the lifted, de-duplicated form of the
SQLite adapter that Hadron and Nanite each carried a copy of. The package
imports no SQLite driver: you open the database with the driver you already
use (its tests use `modernc.org/sqlite`).

```go
package main

import (
	"context"
	"database/sql"
	"log"

	workflowruntime "github.com/hollis-labs/go-workflow/runtime"
	"github.com/hollis-labs/go-workflow-host/sqlstore"
	_ "modernc.org/sqlite" // any database/sql SQLite driver works
)

func main() {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "workflow.db")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1) // one writer connection; add a busy timeout pragma for multiple processes

	if err := sqlstore.Migrate(ctx, db); err != nil {
		log.Fatal(err)
	}
	store, err := sqlstore.New(db)
	if err != nil {
		log.Fatal(err)
	}
	var _ workflowruntime.StateStore = store
}
```

- `Migrate` creates the store's tables, indexes and append-only triggers if
  they are missing and records its own version in `sqlstore_schema_versions`.
  It is safe to repeat and safe on a database that already has the tables. It
  does not detect drift.
- `Schema()` exposes the same SQL as an `fs.FS` for hosts with their own
  migration runner. Hosts that already carry the tables through their own
  history never need to call `Migrate`.
- `Store.WriteTx` and `DBTX` run your own statements in the store's
  `BEGIN IMMEDIATE` transaction, so host tables can commit atomically with a
  workflow write. Inside the callback use only the `DBTX` you are given.
- `WithRunColumns` and `WithHooks` serve hosts whose run table is shared with
  product data under other column names: `RunColumns` renames the run table
  and its id, status, generation and created-at columns, and `Hooks` run
  inside each run and node write, after the canonical statement, rolling the
  write back when they return an error. The defaults reproduce the schema
  `Migrate` creates.

## artifactfs

<!-- artifactfs section is added by its own change -->

## Compatibility

This module is pre-1.0: minor releases may break the exported API. It tracks
go-workflow v0.1.x and pins that exact tag, because go-workflow is itself
pre-1.0 and an interface change there is a minor bump here. Consumers must be
at go 1.26.6 or newer, which is this module's `go` line. Pin an exact version
and read [CHANGELOG.md](./CHANGELOG.md) before upgrading; every breaking
change is listed there.

## Out of scope

- The worker loop, activation scheduling, run diagnostics and any other
  host-layer behavior: this module stores engine state, it does not drive it.
- Databases other than SQLite. The SQL is SQLite dialect; another database is a
  second implementation qualified by go-workflow's conformance suite, not a
  dialect switch.
- Migration history for existing databases and drift detection. Hosts with
  their own migrations keep them.
- Heavy-dependency bridges (LLM providers, MCP clients, sandboxes). Every
  package here stays standard library plus go-workflow.

## Development

```sh
gofmt -l .
GOWORK=off go vet ./...
GOWORK=off go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate. Run Go with `GOWORK=off`:
an ambient workspace file that does not list this module makes `go` refuse to
run here.

## License

MIT — see [LICENSE](./LICENSE).
