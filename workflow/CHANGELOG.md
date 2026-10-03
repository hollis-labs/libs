# Changelog

All notable changes to the `workflow` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`workflow/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.1.0 — 2026-10-03

First release of the workflow module in the libs repository: the `go-workflow` engine as the module root and `go-workflow-host` as `host/`, both moved in with their git history.

### Added

- `workflow` (the module root, import path `github.com/hollis-labs/libs/workflow`): the workflow engine, from `go-workflow` (65 commits of history).
- `workflow/host`: the host layer (`artifactfs`, `sqlstore`), from `go-workflow-host` (13 commits of history).

Each has a `MIGRATION.md` with the old and the new import paths.

### Changed

- The old modules' release tags were not carried over; this is the first release of the module.
- Import paths in code, documentation and tests moved to `github.com/hollis-labs/libs/workflow/...`, mechanically; no symbol was renamed or changed. Places that spell the module path out were rewritten with them: `public-api.txt`, the import guard's module constant and the package-path constants of the two schema generators.
- The import guard treats `host/` like `adapters/`: it may use concrete drivers such as SQLite, and core packages may not import it. The public API snapshot does not cover `host/`, which it never did. Making host API part of the stability contract is an owner decision.
- The nested consumer module `test/external-consumer` moved to `testdata/external-consumer`: a module in this repository may not contain another module outside `testdata/`.
- `go.mod` now also requires `modernc.org/sqlite` v1.58.0 and its indirect set, because `host/` imports them. The core imports none of it, but dependants of the module have those requirements in their module graph. `github.com/google/pprof` (indirect) rose from v0.0.0-20230207041349-798e818bf904 to v0.0.0-20260802141513-ef3492d7dac3, selected by the module graph (`modernc.org/sqlite` v1.58.0 requires it).

---

Below this line is the changelog of the standalone `go-workflow` module, kept as written. Its version numbers belong to that module (`github.com/hollis-labs/go-workflow`), not to this one.

## go-workflow v0.1.0 — 2026-09-04 (standalone module)

The first standalone release of `github.com/hollis-labs/go-workflow` extracts
Hadron's reusable workflow engine at source checkpoint
`c69676ae0cde4baafcc60b31e9269998003a55c7` (`v0.5.0-beta.2`). It preserves
the engine's existing `HADR-*` diagnostics, schema identifiers, wire values,
state and event semantics, digest algorithms, examples, and conformance fixture
meanings while changing the Go module/import path.

Highlights:

- graph-native source, compilation, validation, immutable plan, typed value,
  wait, runtime, verification, memoization, fan-out, retry, reactor, and durable
  compensation contracts;
- public step-kind SDKs and optional adapters for host composition;
- `RunRequired`, `RunComplete`, and compensation-inclusive `RunExhaustive`
  conformance entry points;
- generated graph and execution-plan schemas, public API snapshot, fixture
  manifest, digest goldens, dependency/import guards, and release CI; and
- offline and `runtime/inmemory` reference execution, explicitly not a
  production durability implementation.

This is a pre-v1 compatibility line. Consumers must pin the immutable
`v0.1.0` tag, freeze exact host registries and policy identities, and qualify
their real durable adapters before making production recovery claims. See the
[stability policy](STABILITY.md), [adoption guide](docs/workflow-engine-adoption.md),
and [extraction provenance](docs/history-provenance.md).
