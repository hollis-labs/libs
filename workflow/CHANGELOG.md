# Changelog

## v0.1.0 — 2026-09-04

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
