# Embed the workflow engine in a Go host

The reusable engine boundary is the complete public
`github.com/hollis-labs/go-workflow/...` module. It contains graph and source
contracts, compiler phases, typed values, runtime state machines, waits,
verification, step-kind SDKs and adapters, offline embedding, and conformance
fixtures. It has no dependency on `github.com/hollis-labs/hadron/internal/...`.

Application services, registry publication, authentication, HTTP, MCP, A2A,
databases, workers, and daemon capability profiles are host
composition. They are deliberately not engine dependencies.

## Minimal path

The executable external-package example is
[`offline/adoption_external_test.go`](../offline/adoption_external_test.go).
It performs the complete portable sequence:

1. load one bounded graph-native source with `compile.LoadBytes`;
2. lower it with `compile.Compile`;
3. infer static expression dependencies with
   `compile.InferValueDependencies`;
4. register an exact `StepKind` implementation in `stepkind.MemoryRegistry`;
5. validate the inferred plan with `compile.ValidatePlan`;
6. build an immutable offline manifest and execute it through the ordinary
   runtime with `offline.Execute`; and
7. compile-call the exhaustive `conformance.RunExhaustive` entry point from an
   external package (the example uses an expectation-only wiring fake and is
   not host qualification); and
8. exercise durable compensation directly. A durable host that implements the
   compensation fixture family should use `conformance.RunExhaustive` for its
   final qualification gate.

`offline.Execute` is the smallest embedded host loop. It uses the canonical
binding, recovery, ready-queue, dispatch, wait, and output-finalization paths;
it is not a second interpreter. Use `offline.ExecuteWithStore` to supply a
host store.

## Host-owned bindings

A long-lived host normally supplies these seams:

| Concern | Public contract | Host responsibility |
|---|---|---|
| State | `runtime.StateStore` and narrower enabled-feature interfaces | Durable CAS, idempotency, append-only events, recovery queries, defensive ownership, and process coordination |
| Timers | `wait.ActivationScheduler` | Idempotent `Schedule`/`Cancel` by activation identity and restart recovery |
| Wait endpoints | `wait.Materializer` and `wait.ResponderAuthorizer` | Endpoint lifecycle and authenticated responder policy without persisting raw tokens |
| Kinds | `stepkind.Registry` and `stepkind.StepKind` | Freeze exact name/version/spec implementations before validation and execution |
| Policy | compile policy hooks and runtime authorization interfaces | Bind trusted host identity and evaluate immutable effects/capabilities; graph config cannot authorize itself |
| Artifacts/secrets | `values.ArtifactStore` and adapter-specific secret authorities | Keep artifact bytes and resolved credentials outside persisted value envelopes |
| Verification | `verification.Registry` | Freeze exact verifier contracts with the plan and runtime catalog |

`runtime/inmemory.Store` is the public concurrency-safe reference store used by
offline and contract qualification. It implements the runtime storage
semantics, but its durability is only the lifetime of one Go value. It does not
survive restart, reopen across processes, or satisfy a host's crash-recovery
promise. `runtime/runtimetest` is a deprecated source-compatible alias; new
code imports `runtime/inmemory`.

The engine does not prescribe a worker pool, database, HTTP server, principal,
registry, or scheduler implementation. A host must keep one exact plan and
kind catalog behind recovery, load nodes from that pinned plan rather than a
mutable source, and fence effectful execution with its own policy and durable
claims.

## Step-kind catalog

Every kind publishes an immutable `stepkind.StepKindSpec` before execution:
exact name/version, config/input/output schemas, effects, capabilities,
idempotency, retry safety, cancellation, observation, suspension, and optional
lifecycle hooks. `stepkind.Resolve` never selects a latest version when more
than one version exists. Register all implementations, verify the advertised
specs, then treat the registry as frozen for a plan's lifetime.

`stepkind/stepkindtest` provides public application-neutral fake kinds for
downstream tests. Concrete packages under `adapters` are optional
embeddable capabilities; importing an adapter does not enable it. The stock
Hadron daemon's six-kind profile is a product-host choice, not an engine limit.

## Schema and compatibility policy

The public formats are versioned independently and matched exactly:

- graph source uses the generated schema at
  [`graph/schema/workflow.schema.json`](../graph/schema/workflow.schema.json);
- compiled plans carry `compile.ExecutionPlanSchemaVersion` and immutable content
  digests;
- offline artifacts carry `offline.ManifestSchemaVersion`; and
- step kinds, definitions, verifiers, and adapters use exact declared
  versions and immutable schema/effect metadata.

Do not infer compatibility from a digest, choose a latest version, or accept an
unknown schema version. Additive Go APIs and optional schema fields may be
introduced compatibly. Removing or changing an exported contract, persisted
meaning, enum, required field, or exact-version behavior requires an explicit
versioned contract decision, updated conformance fixtures, and migration or
compatibility evidence appropriate to that boundary.

[`public-api.txt`](../public-api.txt) snapshots exported Go declarations for
every public package, including adapters. The import/API guard fails on
unreviewed drift, host dependencies, or unapproved core dependencies. The
snapshot is change control; downstream consumers must pin an immutable module
release. It is not a claim that every concrete adapter is enabled by every host.

Regenerate the graph schema and deliberately refresh a reviewed API change:

```sh
go generate ./graph ./compile
UPDATE_PUBLIC_API=1 go test ./internal/importguard
git diff -- graph/schema compile/schema public-api.txt
```

## Conformance

Use the embedded, bounded fixture store and isolated factories:

```go
conformance.RunRequired(t, conformance.EmbeddedFixtures(), requiredHost)
conformance.RunComplete(t, conformance.EmbeddedFixtures(), completeHost)
conformance.RunExhaustive(t, conformance.EmbeddedFixtures(), compensationHost)
```

`RunRequired` covers compiler/source maps, state values, scheduler and control
flow, waits, and step-kind metadata. `RunComplete` additionally covers
verification and memoization while preserving the pre-compensation host
contract. `RunExhaustive` requires `CompensationHost` and adds the durable
compensation fixture family; it is the final qualification entry point for a
host that enables compensation. `Host` and `RunAll` remain deprecated
source-compatible names for the original required set so existing adopters are
not silently broken.

Fixture inputs are opaque to the harness. Each factory must create an isolated
runner that exercises the downstream implementation; merely matching the
fixture's expected outcome only tests harness wiring, not conformance.

## Production adoption checklist

A production host is ready only after all of these stay true:

- its `go.mod` pins one immutable `go-workflow` release and contains no local
  `replace` directive;
- the module dependency graph has no host-internal or sibling-application
  dependency;
- graph, plan, value, wait, runtime, and step-kind meanings remain stable under
  the API/schema guards and complete conformance suites;
- the host uses durable implementations for every enabled store, timer, wait,
  artifact, cleanup, policy, and executor seam and proves restart recovery;
- exact step-kind and verifier catalogs, policy identity, source/plan identity,
  schemas, and adapter contracts are frozen and attested;
- external effects are protected by durable claims, idempotency, authorization,
  cancellation, and bounded cleanup; and
- real host adapters pass `RunRequired`, `RunComplete`, and, when compensation
  is enabled, `RunExhaustive`. An expectation-only runner does not qualify a
  host.

The in-memory store is never acceptable evidence for a production durability
claim. Nanite, Hadron, Torque, Cerberus, and other applications are downstream
consumers and own their production composition.

See the module's [stability policy](../STABILITY.md) before upgrading.
