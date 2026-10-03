# Conformance

What `hitltest` checks, what was actually run, and what has not been.

## Kit

- `hitltest.Fixtures()`: one JSON document per file under `hitltest/testdata/{valid,invalid}`. Each is an envelope `{def, name, [rejects_because], doc}`. Every valid fixture must validate against its `def`; every invalid one must be rejected **at the JSON Pointer it records** (a document rejected for the wrong reason is a vacuous pass). Pointers name the failing instance location; a missing required member is reported at its parent object.
- `hitltest.Scenarios()` and `RunConformance(t, adapter, caps...)`: operation sequences with expected results, run against a wire-level `Adapter` (JSON bytes in, JSON bytes out). The runner also validates every response against the schema bundle and checks that observed state changes per item are a path allowed by `CanTransition`. Scenarios whose capability is not declared are **skipped with a message that says so**; a skip is not a pass.
- `RunStoreContract(t, newStore)`: atomic idempotent `Create`, compare-and-set `Swap` with exactly one winner under concurrency, immutable terminal records, the `CheckSwap` invariants, `DueForExpiry`.
- `hitltest.NewServiceAdapter(svc)` adapts the reference `Service`. `TestConformanceRejectsBrokenAdapters` proves the suite can fail (ignoring expiry, clamping `wait_ms`, overwriting a resolution, bodyless errors, reopening a terminal item are each detected).

Capabilities: `expiry-enforcement` (scenarios enqueue an item whose `expires_at` is already past, so the adapter must accept a past deadline) and `caller-isolation` (advisory scopes only).

## Scenarios and the Tangent test that already asserts the behavior

Paths are relative to `apps/tangent` (`internal/hitl/service_test.go` unless noted). "read" means the test was located and read by name in the briefs, not re-executed.

| Scenario | Tangent evidence |
|---|---|
| `enqueue-idempotent-replay` | `service_test.go:24` TestEnqueueAllocatesOneGlobalFIFOAndScopesIdempotency |
| `idempotency-conflict` | `:24`, `:611` TestEnqueueRejectsConcurrentGenericIdempotencyWinner |
| `idempotency-scope-per-application` | `:24` (same key in another application_id) |
| `get-pending` | `internal/envelope/extensions/hitl_item_test.go` hitlRetrievalFixture |
| `get-terminal` | `:766` TestWithdrawResolveRaceAndDistinctTerminalCauses |
| `await-timeout` | `:658` (await does not mutate) |
| `await-terminal` | `:658` |
| `await-out-of-range` | `service.go` Await (`maximumWait` check); no test named |
| `withdraw-then-withdraw` | `:766`; `service.go` Withdraw |
| `resolve-then-withdraw` | `:766` |
| `withdraw-then-participant-resolve` | `:766` |
| `withdraw-stale-expected-revision` | `service.go` Withdraw (`staleError`); no test named |
| `terminal-immutability` | `:457` (concurrent acknowledgement has one immutable winner) |
| `caller-isolation` | `service.go` `inspectHITL`; Tangent answers `unauthorized` inside one authority, `not_found` across authorities |
| `expiry-refuses-late-respond`, `expiry-refuses-late-withdraw`, `expiry-sweep` | **none.** No Tangent test drives expiry through the public surface: `ExpireInteraction` is host-privileged with no production callers. |

Tangent cannot yet drive the expiry scenarios, and does not enforce the `expires_at` it accepts; that is why expiry is a capability. **Tangent's adapter is adoption work and does not exist.** Tangent must first raise its `go` line to 1.26.6 before it can import go-hitl.

## What was run (2026-09-29)

- `go test -race -count=1 ./...` over all packages, and `-count=20` on the state machine, the store contract, the conformance run, and the expiry-versus-respond race tests. Results are in the session record, not asserted here.
- The **schema-level** Tangent cross-check, `HITL_TANGENT_SCHEMA=<abs path to request.schema.json> go test ./schema -run Tangent -v`, was run against `apps/tangent/internal/envelope/extensions/packages/tangent.hitl/hitl-item/request.schema.json` (read only): all 12 copied definitions and the 3 profile definitions are canonical-JSON equal to Tangent's (profiles modulo the added `x-hitl-tier` tag), and 16 of Tangent's `examples` validate against their mapped Core definitions. Two `HITLResolutionCommandV1` examples are deliberately not mapped (that definition stays in Tangent). Without the environment variable the two tests **skip loudly**; `make tangent-check` runs them and fails if they skip.
- The reference `Service` over `memstore` passes every scenario.

## What was not run

- Tangent's own Go implementation, tests or MCP tools were not executed. No claim is made that its service behaves as these scenarios expect, only that its published schema and examples are compatible with the Core definitions.
- No other application (Torque, Nanite, Hadron, Fragments Engine, Cerberus) was run against the kit.
- The REST `/api/hitl` shapes were not read; the scenarios are MCP-shaped (`tangent.hitl_*`).
- Cross-caller Get in Tangent answers `unauthorized` within one authority (`internal/interaction/scope.go` `denial`), read but not executed; the scenario accepts `not_found` or `unauthorized`.
