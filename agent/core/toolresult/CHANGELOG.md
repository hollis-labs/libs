# Changelog

All notable changes to go-toolresult are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- Root package `toolresult`, lifted from Nanite's `internal/tool` result cache
  and made store-independent:
  - Pure functions: `Preview` (JSON-pointer-labeled or head-and-tail text),
    `BudgetForWindow` and `BudgetOptions`, `Select` (RFC 6901), `ReadPage`
    (UTF-8-safe, always makes progress), `SearchPage` (RE2, budgeted,
    continuable), `CutUTF8`.
  - `Cache` over a `Store` port: `Present`, `Put`, `Read`, `Search`, `Purge`,
    `HandleFetch`, `HandleSearch`, with `Config`, `Meta`, `Pointer`, `View`,
    `FooterData` and `DefaultFooter`. Header, footer and tool text are pinned by goldens
    captured from copies of Nanite's functions (see below).
  - `ToolSpec`, `FetchSpec` and `SearchSpec`: the agent-facing tool
    definitions as data.
  - `ErrNotFound`, `ErrExpired`, `ErrBodyNotStored`, `ErrEmptyScope`.
- `memstore`: in-memory `Store`.
- `sqlstore`: `database/sql` (SQLite dialect) `Store` with a configurable
  `Table`; the defaults match Nanite's `tool_result_cache`, and
  `Table{Name: "wiki_result_cache", ScopeColumn: "caller_id"}` matches Loom's.
  `DDL` renders the schema.
- `storetest.Run`: conformance suite for any `Store`.
- `TestGoldenParityWithNanite` pins 24 outputs captured by running copies of
  Nanite's functions in a scratch module; the harness is not committed. Only
  those pinned outputs are byte-for-byte claims. Fuzz tests for paging, pointer selection, preview and search, and property tests for
  `CutUTF8` and paging.

### Known limitations

- Parity with Nanite was checked against a copy of its functions, not against
  Nanite in place. It is not verified that Nanite's `*sql.DB` passes into
  `sqlstore` unchanged. Nanite's adoption should confirm both.
- `RunPurger` (a periodic purge loop) is not built; call `Cache.Purge`
  yourself.

### Not carried over from Nanite

- `StoreResult`, the legacy byte-slice `Fetch`, the legacy `Search` and
  `truncateAtBoundary` (dead or superseded), the soft-truncation setting, and
  the disk spill.
