# Changelog

All notable changes to go-workflow-host are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `sqlstore`: a SQLite `runtime.StateStore` for go-workflow v0.1.0, lifted from
  the copy Hadron and Nanite each maintained. `Store` implements
  `StateStore` and the 20 companion runtime store interfaces (cancellation,
  child-terminal waits, control flow, external operations, fan-out,
  memoization, pins, output reuse, value records, reactors, recovery, replay,
  node inputs, retry, run controls, run policy, scheduler resources, services,
  waits, compensation).
- `sqlstore.New`, `Store.DB`, `Store.WriteTx` and `DBTX`: construct a store on
  an open `*sql.DB` and run host statements inside the store's write
  transaction.
- `sqlstore.Migrate` and `sqlstore.Schema`: embedded final-shape DDL (tables,
  indexes and append-only triggers), applied create-if-missing with its own
  `sqlstore_schema_versions` table, or exposed as an `fs.FS` for a host's own
  migration runner.
- `sqlstore.WithRunColumns` / `RunColumns` and `sqlstore.WithHooks` / `Hooks`:
  support a run table shared with host product data under other column names,
  and mirror run and node writes inside the write transaction. Defaults
  reproduce the Hadron schema and SQL exactly.
- Qualified by `conformance.RunExhaustive` on a fresh database per fixture, on
  the default schema and on a host-shaped run table with foreign keys enforced.
- artifactfs: package `github.com/hollis-labs/go-workflow-host/artifactfs`, a crash-safe
  local-filesystem `values.ArtifactStore` merged from two independent host implementations
  (`New`, `Option`, `WithExternal`, `OwnerClaimAuthorizer`). The authority string is a required
  argument and part of every persisted reference; the on-disk format and artifact ids are
  unchanged from both implementations (pinned by golden id vectors). Where the two disagreed
  the stricter behaviour was taken:
  - `Stat` verifies the full payload digest, not only the size.
  - Symlink components are rejected on every Stat, Open, Delete and Cleanup path, not only in `New`.
  - The parent directory is fsynced after `Delete` and after each cleanup removal.
  - References of another authority are routed only to delegates approved with `WithExternal`;
    an unknown but well-formed authority is `ErrArtifactAuthority` for Stat, Open, Delete and
    Cleanup (external kind), and a delegate reference without `RetentionExternal` is
    `ErrArtifactRetention` on Delete. Malformed references of the local authority stay
    `ErrArtifactInvalid`.
  - `Put` checks a non-canonical `Store` (`ErrArtifactAuthority`) before authorization, then
    authorizes, then rejects a canonical foreign store.
