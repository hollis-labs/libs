# go-workflow

A deterministic, embeddable workflow engine. It owns the portable graph, the
compiler and execution plan, the runtime state machine, and the wait,
typed-value, artifact, retry, fan-out, compensation, verification, memoization,
offline, schema and conformance contracts. The host owns durable storage,
scheduling, authentication, policy, transport, UI, and which step kinds it
enables. There is deliberately no root Go package — import only the packages
your host needs.

## Start Here

- `docs/workflow-engine-adoption.md` is the minimal embedding path and the
  host-owned seams; start here before reading code.
- `STABILITY.md` is the pre-v1 compatibility line; `DEPENDENCIES.md` is the
  enforced core/adapter import boundary.
- `public-api.txt` is the exported-declaration change guard.
- `graph/` is the portable graph; `compile/` lowers it to an execution plan.
- `runtime/` is the state machine; `wait/` owns suspension and resume.
- `values/`, `gate/`, `verification/` and `offline/` own the typed-value,
  gating, verification and offline contracts.
- `adapters/` holds the concrete step kinds and is outside the engine core.
- `conformance/` is the suite a host runs to prove its integration.

## Commands

```bash
make vet
make test
make race
```

Do not run `make check` in this working tree. It also runs `make generate`
(`go generate ./...`), `make tidy-check` (`go mod tidy`) and `make test-external`
(a nested `go mod tidy`), each of which rewrites tracked files — schemas,
`go.mod` and `go.sum` — and then asserts the diff is empty. Those are CI
guards; CI runs `make check` and is the right place for them.

## Boundaries

Never resolve a workflow contract, schema, step kind, verifier or plan by a
floating version. Hosts pin an exact module tag and freeze exact registries per
plan identity, because a plan that resolves differently on replay is no longer
deterministic.

`DEPENDENCIES.md` is enforced, not advisory: the engine core is every package
except `adapters`, and its imports are limited to the standard library, other
non-`adapters` packages here, `gopkg.in/yaml.v3`, `jsonschema/v6` and
`expr-lang/expr`. `goja` and everything else belong behind `adapters`. Putting
a transport or product dependency in the core ends the engine's portability.

Determinism is the product. Digests are relocatable and stable under map
ordering, diagnostics are structured, deterministic and fail closed, and
validation order itself is stable — `TestActivationDigestsAreDeterministicRelocatableAndMapOrderStable`,
`TestActivationDiagnosticsAreStructuredDeterministicAndFailClosed` and
`TestActivationValidationOrderIsStable` are the guards. Anything that leaks
map-iteration order or wall-clock into a digest breaks replay.

The event log is append-only, atomic, ordered and immutable
(`TestAppendOnlyEventsAreAtomicOrderedAndImmutable`), and adapter observations
are masked before persistence
(`TestAppendMaskedEventMasksAdapterObservationsBeforePersistence`) so adapter
payloads cannot leak secrets into durable history.

Activation is a privilege boundary: expressions cannot reference steps, and
privileged authority tokens are rejected
(`TestActivationExpressionsCannotReferenceSteps`,
`TestActivationRejectsPrivilegedAuthorityTokens`).

`public-api.txt` is a snapshot guard. If your change moves it, that is a
deliberate API change to be reconciled against `STABILITY.md`, not a file to
regenerate quietly.
