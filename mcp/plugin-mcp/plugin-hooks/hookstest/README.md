# hookstest

A standard-library-only conformance suite for hook dispatchers and an in-process
harness for plugin authors. Requirements have stable IDs local to this package.
The reference `Registry` + `Engine` adapter runs every requirement without waivers.

## Host migration gate

Implement `Factory`, `Dispatcher` and `Scope` over the host's actual hook path.
The factory creates an isolated dispatcher with the supplied catalog and execution
limits, and rejects invalid declarations before callbacks run. Map native results
to `pluginhooks.DispatchResult` and native failures to the exported `errors.Is`
sentinels. `Handle` is an opaque host token; preserve its identity across removal
and reload. All callbacks and nested dispatches retain their supplied context.

```go
func TestHookConformance(t *testing.T) {
    hookstest.Run(t, hostFactory)
}
```

For library comparison or plugin unit tests, `hookstest.NewEngineAdapter` is a
ready-made factory. A migration test's factory must exercise its host integration.
Each requirement starts fresh dispatchers; tests run sequentially and use channels
to observe lifecycle events. `WithBound` changes the observation/cleanup bound
(default three seconds), while the timeout probes keep their explicit budgets.
Factories and callbacks must obey context cancellation and avoid indefinite
blocking. Arbitrary in-process Go code cannot be forcibly terminated by the suite.

A host with a documented gap can add `hookstest.Waive("R06", "legacy dispatcher
has no whole-budget deadline; migration tracked by the host")`. Every waiver
prints `WAIVED`, its ID and reason as a skipped subtest. Unknown IDs and empty
reasons fail the run. Waivers record migration gaps; they do not certify full
conformance. `Requirements()` returns a detached list for migration reports.

Requirement IDs are stable within this package. New requirements are appended;
existing IDs are never renumbered or reassigned. Hosts may persist these IDs in
waivers and migration reports, and `Waive` rejects unknown IDs.

| ID | Requirement |
| --- | --- |
| R01 | Action priorities, explicit zero, default priority and stable ties across owners |
| R02 | Waterfall ordering and accepted output passed to the next filter |
| R03 | Parallel completion with outcomes retained in priority order |
| R04 | Explicit open/closed policies, failed-output rejection and continuation |
| R05 | Bail cancellation and approval sentinels stop even under open policy |
| R06 | Per-handler deadlines, whole-dispatch budget and caller cancellation |
| R07 | Panic recovery for sequential/parallel/bail actions and waterfall filters |
| R08 | Once under concurrent dispatch, attempted failures and canceled admission |
| R09 | Foreign/stale handles, name reuse and generation tombstones |
| R10 | Unload cancellation, bounded wait, owner sweep and late-output invalidation |
| R11 | Context-carried depth limits with a fresh depth for independent dispatches |
| R12 | Capacity held until a timed-out handler actually finishes |
| R13 | Private ingress, per-handler payload/metadata, accepted output and egress |
| R14 | Deep field masking, hidden-field preservation and visible mutable merge |
| R15 | Catalog validation, allowlists, options, remote permission and duplicate names |

The adapter surface covers synchronous actions and filters. Async/post-commit
scheduling, transport, schema compilation, authorization policies and application
adoption need additional host integration tests. The suite does not grant authority
or choose a host's schemas, hook vocabulary or application policy.

## Plugin author harness

`NewHarness(factory, catalog, executionConfig)` uses an explicitly authored test
catalog. `Load(ctx, scopeConfig, register)` gives the registration callback only
its generation's `Scope`. The returned scope can be disposed for an early unload.
If registration returns an error, partial registrations are swept before `Load`
returns. Use the harness's `EmitAction` and `ApplyFilters` to drive plugin callbacks.

```go
h, err := hookstest.NewHarness(hookstest.NewEngineAdapter, catalog, executionConfig)
if err != nil { t.Fatal(err) }
t.Cleanup(func() {
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    if err := h.Close(ctx); err != nil { t.Error(err) }
})

scope, err := h.Load(ctx, pluginhooks.ScopeConfig{
    Owner: "example", Generation: "test-load", Hooks: []string{"example.ready"},
}, func(scope hookstest.Scope) error {
    _, err := scope.AddAction("example.ready", "observe", pluginhooks.Options{}, handler)
    return err
})
if err != nil { t.Fatal(err) }
_, err = h.EmitAction(ctx, "example.ready", json.RawMessage(`{}`), nil)
if err != nil { t.Fatal(err) }
if err := scope.Dispose(ctx); err != nil { t.Fatal(err) }
```

`Close` disposes scopes in reverse load order, attempts every cleanup and joins
failures before closing the dispatcher. A timed-out close can be retried; loading
after close is refused. Use a fresh cleanup context so a test's canceled request
cannot skip teardown. This harness does not start external plugin processes.

From the repository root run `./scripts/check.sh` for the full gate. It is slow
and computationally heavy, including full race tests and 20 repeated conformance
and harness runs. During development use `go test ./hookstest` and `go vet ./...`.

## Remote seam requirements

Factories implementing `RemoteScope`, `RemoteDispatcher`, and
`RemoteExecutionDispatcher` also run these
requirements against an in-memory fake `RemoteHandler` transport. The reference
passes with zero waivers. A factory lacking the remote extension must explicitly
waive the affected IDs with a reason; no implicit waiver is provided.

| ID | Requirement |
| --- | --- |
| R16 | Remote-only scopes, remote_ok, latency ceiling and remaining-budget admission |
| R17 | Structured result branches, deliberate bail vetoes and no text/sentinel heuristics |
| R18 | Correlation/digest/context snapshots, field views, connection/unload and generation fences |
| R19 | Verified parent bindings, one shared depth guard, inherited deadline/budget/trace, direct and indirect active-registration cycles |
| R20 | Observation batch caps, whole-envelope validation, ordered independent outcomes and private item leases |
| R21 | Default-off notifications, queued receipts, rollback without sends, and no breaker success |
| R22 | Transport failure accounting, open skip and half-open recovery under existing policy |

R01-R15 retain their existing identities. Separate broken-adapter probes prove
that every new requirement rejects a
specific bad integration. Run the same `./scripts/check.sh` gate.
