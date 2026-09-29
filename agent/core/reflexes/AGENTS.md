# go-reflexes

DB-agnostic sense, integrate, act steering engine: reflex triggers, combining algorithms, cooldown cascade and a handler registry.

It is not a store, a state collector, a seed catalog or an adapter for any one application: the host owns those and implements the small seams.

## Start Here

- `reflexes` package (module root) — the importable API; its `doc.go` is the package documentation. `engine.go` (`Run`), `resolve.go`, `executor.go`, `evaluator.go`, `telemetry.go`, `recurrence.go`, `types.go`.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Standard library only, and no hollis libs: `go list -deps ./...` shows nothing outside the stdlib and this module. Do not import `go-hooks`; the plugin seam is `Filters`, not `Hooks`, so it cannot collide with go-hooks' vocabulary.
- Field names and JSON tags of `Reflex`, `ActionKind`, `State`, `MessageSignal`, `EventSignal`, `AppliedAction(s)`, `CandidateOutcome` and the trace record match Nanite's on purpose (aliasing); the traces are pinned byte for byte by `TestRunEquivalence_*`.
- Handler phases: `PhaseResolve` errors are swallowed and the action still fires (`TestPhaseResolve_ErrorSwallowed_ActionStillFires`); `PhaseAfterEmit` errors happen after the firing is counted, and every fired action is attempted (`TestPhaseAfterEmit_ErrorAfterFired`). Do not "fix" either.
- `dispatch_to_agent` is a staged decision output, not a handler; the caller acts on `Result.Applied`. `resume_loop_run` is the only kind needing an app handler, registered at the composition root. No `Resumer` interface.
- Resolve fails open to `all_applicable` on a kind-lookup error and Warn-logs it (`TestResolve_KindLookupFailure_DefaultsToAllApplicable`, `TestResolve_KindLookupFailure_LogsWarning`).
- `Result.Considered` counts candidates after `Kinds`/`ExcludeKinds` (`TestRun_ConsideredCountsCandidatesEvenWhenNoneFire`); a host gating a blind retry depends on it.
- Numeric signals: 0 means unknown. Keep the ints; do not change the semantics in v0.x without a decision.
- Out of scope: seeds, `StateCollector`, SQL, `Validate`/JSON Schema for specs, reminders, reactions, loop detection, hook-to-reflex ingest.
