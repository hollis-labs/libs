# go-workflow-host

Host-side companions for go-workflow: a SQLite runtime.StateStore (sqlstore) and a local-filesystem values.ArtifactStore (artifactfs).

It is not: a place for the worker loop, activation scheduling, run diagnostics, other databases, or any package that pulls a dependency beyond the standard library and go-workflow. Host behavior stays in the host; this module only stores and serves engine state.

## Start Here

- `sqlstore/` — the SQLite `runtime.StateStore`; its `doc.go` is the package documentation, `assertions.go` lists every interface it must satisfy, `schema/*.sql` is the embedded DDL.
- `README.md` — one section per package; the `sqlstore` Go code fence must keep compiling (there is deliberately no `examples/` dir and no root package).
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
GOWORK=off go vet ./...
GOWORK=off go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Always run Go with `GOWORK=off`: an ambient `go.work` that does not list this module makes `go` refuse to run.
- Non-test code is standard library plus `github.com/hollis-labs/go-workflow` (exact tag, pre-v1) only. `modernc.org/sqlite` is a test-only dependency; no non-test file may import a driver.
- The lifted files keep their Hadron names (`workflow_state_*.go`, `workflow_compensation.go`) so diffs against Hadron stay cheap. Keep bodies close to the source; do not restyle them.
- Inside a `Store.WriteTx` callback (and inside `Hooks`) use only the `DBTX` you were handed, never `Store.DB`: the database is normally one connection and the transaction already holds it. Guarded by `TestWriteTxConcurrentOnSingleConnection`.
- A hook error must roll back the canonical write. Guarded by `TestHookErrorRollsBackCanonicalWrite`.
- `RunColumns` names are spliced into SQL text and must stay validated plain identifiers. Guarded by `TestWithRunColumnsRejectsNonIdentifiers`. With default options the SQL text must stay identical to Hadron's.
- Append-only and immutability triggers in `sqlstore/schema` are load-bearing and travel with their tables; never drop one to make a test pass.
- `Migrate` is create-if-missing only. It must stay safe on a database that already has the tables (`TestMigrateOnDatabaseWithExistingTables`). Editing an applied schema file breaks existing databases: add a new numbered file instead.
- A new `runtime.*Store` method in a go-workflow bump must be implemented, and the interface added to `sqlstore/assertions.go`; `TestWorkflowLibraryProductionStoreRunExhaustive` and `TestRunExhaustiveOnHostRunColumnShape` qualify the store against the go-workflow conformance suite.
- Do not commit tests that assert a table count or that the DDL equals an application's migrations; those are release-time checks, not suite content.
- Each package is independent: no package imports a sibling, so a future split stays a directory move.
- `artifactfs`: the authority string is persisted identity (part of every reference and artifact id); it has no default and must never change for an existing root. The on-disk format (manifest version 1, artifact id, URI shape, directory and file names) is frozen; the golden id vectors in `artifactfs/golden_test.go` pin it, and a format change needs a manifest version bump.
- `artifactfs` takes the stricter side wherever the two source implementations disagreed (full-digest `Stat`, symlink rejection on every path, parent fsync after `Delete`); loosening one is a security decision recorded in the CHANGELOG, not a convenience. Do not add `%w` cause text to `values.ArtifactError` messages.
