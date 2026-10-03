# plugin-hooks

Standard-library-only Go module for host-owned hook catalogs and plugin-generation
registrations. No dependency on plugin-sdk or go-hooks. Hosts supply identity,
authorization, schema validators and transport.

## Registration API

Create a `Registry` with `NewRegistry(Catalog)`. Every declaration explicitly names
its kind, mode, schema, compiled validator, resource limits, allowed error policies,
version and digest. There are no policy presets. `RemoteOK` must be explicitly set;
`ValidateInput` and filter `ValidateOutput` must be concurrency-safe host callbacks.
The registry checks declaration structure; it does not compile or verify JSON Schema.

The trusted host calls `NewScope(ScopeConfig)` with an owner, a fresh load generation,
and an explicit hook allowlist. Give the plugin only its `Scope`. An empty allowlist
permits no registration. Host-owned core hooks use an explicit owner too.

Use `Scope.AddAction` or `Scope.AddFilter` with a hook name and registration name.
Names are unique across hooks within the scope. Duplicates fail; removal permits
reusing a name with a fresh handle. Priority omission means 10; a pointer to zero
means 0. Lower priorities come first, with ties in registration order. Timeout
omission uses the declaration's cap/default; explicit non-positive or excessive
values fail. Error policy and view are resolved and retained in `Registrations()`.
Required views cannot be bypassed. An optional supplied schema digest must match.

`Scope.Remove(handle)` can remove only its own registrations. `Registry.Remove`
and `RemoveByPlugin(owner, generation)` are host operations. Handles are opaque,
registry-specific, never reused and safe against stale removal. Removal cancels
active calls and prevents starts from existing snapshots. It cannot undo an action's
external effects. `RemoveByPlugin` sweeps registrations but leaves the scope open.

Unload with `Scope.Dispose(ctx)`, even when plugin shutdown fails. Disposal atomically
closes registration and starts, cancels active work, invalidates late results, and
waits only within the context's budget. A timed-out disposal remains closed; a later
call may wait again. Reload must use a fresh generation. Disposed generation keys
remain reserved for the registry lifetime.

Once means at most one attempted invocation, atomically claimed after execution
capacity is acquired. Cancellation before start does not consume it; a claimed
attempt is retired regardless of its result. Retirement alone preserves that
attempt's result; explicit removal/disposal invalidates it. No plugin callbacks
execute under registry locks.

## Execution API

Create one `Engine` per registry with `NewEngine(registry, ExecutionConfig{})`.
Provisional defaults from the draft ADR are 64 active calls, 8 per owner generation,
depth 8, 256 queued dispatches and 4 workers. Set explicit positive limits to tune
these; negative values fail. Call `Shutdown(ctx)` to close queue admission, cancel
work and release worker goroutines. It cannot terminate uncooperative Go handlers.

`EmitAction(ctx, hook, payload, metadata)` implements the catalog mode. Sequential
actions observe independent copies in priority order; parallel actions admit in
that order with finite concurrency and return outcomes in registration order.
Parallel ordinary closed errors are reported after admitted handlers settle; they
cannot roll back observations. Bail gates stop on `ErrCancelled` or
`ErrApprovalRequired` even with open policy. Approval must be resolved by the host
before revalidating and dispatching again; the engine does not grant permission.

`ApplyFilters` implements waterfall transformations. An open failure preserves the
previous accepted value; a closed failure returns an error and no value. The full
merged output passes the declaration's output validator before acceptance. Changes
must be inside both `MutablePaths` and the selected view. Hidden fields remain
intact. Array projections preserve positions with null placeholders; container
shape/length remains visible. Array resizing requires visibility and mutability of
the whole array. JSON numbers retain their precision during copy/merge. Validators
are synchronous trusted host callbacks: they must be bounded, concurrency-safe,
and non-mutating. JSON Schema compilation is unavailable in the library.

Handler deadlines use the earliest dispatch/caller deadline and registration
limit. Waiting for capacity consumes those limits. Panic is contained at every
handler boundary, and each outcome identifies hook, owner, generation, registration,
handle, invocation and error class. Errors use `errors.Is`. Statuses distinguish
`success`, `cancelled`, `approval_required`, `failed_closed`,
`completed_with_open_errors`, `queued`, and `caller_cancelled` (including whole
budget expiry). Dispatch-level input/depth/engine failures return an error before
any handler starts. Veto sentinels have special permission meaning only in bail
mode; other modes apply their ordinary declared error policies.

Capacity is acquired before a once claim or goroutine launch and released only on
actual handler completion. Per-dispatch hook parallelism, registry-wide limits and
owner-generation limits continue to count timed-out handlers. Unavailable handlers
follow their resolved policy; whole-dispatch cancellation can never become success.
Depth is carried in handler contexts and preserved through async enqueue. Nested
calls also consume capacity: hosts must tune limits for intended nesting. Dropping
the supplied context bypasses the cooperative depth guard.

Async dispatch returns `Queued` with `Future.Await(ctx)`. Queue admission is finite,
FIFO, visibly rejects overload, and includes queue wait in the budget. Work detaches
from the request's cancellation after admission but retains context values/depth
and the engine lifetime. Independent workers have no completion-order guarantee.
Future results copy their bytes and outcome slice for every reader.

After-commit declarations require `PrepareAfterCommit`, which copies/validates and
returns a one-shot capability. The host calls `Commit()` only after confirmed commit,
or `Rollback()` on failure. Transaction wait consumes the dispatch budget. Commit
returns an async receipt; overload must be surfaced after the transaction. This is
in-memory scheduling, without retries or durable delivery. Always resolve a pending
capability: the engine has no transaction driver.

## Known limitations

Filter diffs compare JSON numbers by their literal text. Re-encoding an untouched
number can be rejected as invalid output when its path is immutable: for example,
`1.50` becoming `1.5`, `1e3` becoming `1000`, or a large integer being rounded by
`float64`. Go handlers should preserve untouched values byte-for-byte (for example,
using `json.RawMessage`), or change only paths declared mutable and visible.

## Current boundary

All six declared modes execute. Remote wire, schema compiler,
durable delivery and consumer adoption are unavailable. The private
[TypeScript twin](ts/README.md) implements the execution/lifecycle contract and
shares JSON conformance fixtures with Go; breaker/telemetry parity is deferred.
There are no compatibility aliases or release tags yet.

## Checks

Run fast checks with `GOWORK=off go test .`, `GOWORK=off go vet ./...` and
`golangci-lint run --allow-parallel-runners`. At final review run
`./scripts/check.sh`; the full gate is slow and computationally heavy. It runs
`go test -race -count=1 ./...`, matches CI checks and repeats concurrency-heavy
lifecycle tests with race instrumentation. Install local hooks with `lefthook install`
if desired; the tracked configuration alone installs nothing.

## Catalog introspection and registration preflight

`json.Marshal(registry.Catalog())` produces a sorted `catalog_version` document
with inline JSON Schema objects, mutation/view rules, version/deprecation data,
explicit remote policy, resource limits and schema digests. Validator functions
are excluded. `Catalog.Document()` returns detached `CatalogDocument` data;
`Definition` also marshals to this form. Millisecond fields preserve fractional
limits. This is introspection, not an executable catalog: hosts still compile
validators and validate declarations when installing a registry. Catalog version,
Go module version, and subprocess protocol version are independent.

Custom declarations require exactly `plugin.<owner_namespace>.<subject>.<event>`.
The trusted publisher supplies `Definition.OwnerNamespace`, which must match the
name's namespace segment. Every declaration still requires schemas, validators,
limits and explicit policy. The namespace grants no authority: the host assigns
scope allowlists, including any subscriptions to another namespace's declarations.
There is no plugin-controlled catalog extension API or lossy plugin-ID conversion.

A host can call `scope.ValidateRegistration(hook, registrationName, kind, options)`
for each manifest hook/filter before attaching handlers. It applies the same kind,
allowlist, digest, view and resolved-options policy as `AddAction`/`AddFilter`.
It returns `RegistrationValidation` with resolved options and structured warnings.
It reserves neither names nor capacity; adding handlers rechecks policy and
atomically enforces duplicate names and handler limits. Manifest parsing, identity,
authorization and remote transport remain host responsibilities.

Deprecation requires since, reason and planned removal; replacement is optional
and must be a distinct canonical name. Preflight returns `RegistrationWarning`
data, and successful registrations retain the same warnings in `Registrations()`
for the host to surface. No warning callback or plugin code runs under locks.
Deprecation never redirects a registration. At the removal release the publisher
omits the entry from its supported catalog; both scope creation and registration
then fail with `ErrUnknownHook` naming the removed hook. The library does not guess
whether a host release has reached an arbitrary removal-version string.

`AgentLifecycleMappings()` returns the explicit eleven-event adapter table for
native go-hooks names. It does not install aliases, translate payloads, execute
commands or change permission behavior. The registry rejects native names such as
`Stop` and `PreToolUse`. Hosts must implement and validate the adapter separately;
`Stop` maps to `turn.stopping`, and `PreCompact` remains a non-veto observation.

## One discovery endpoint

Mount `NewDiscoveryHandler` once behind the host's authentication, request budget
and permitted-subset policy. Hosts adapt the hooks, contributions and capabilities
catalogs with the standard-library-only `Provider` interface; this module imports
neither of the other catalog modules. For example, with host-provided
`contributionProvider` and `capabilityProvider`:

```go
handler, err := pluginhooks.NewDiscoveryHandler(map[string]pluginhooks.Provider{
    "hooks": registry,
    "contributions": contributionProvider,
    "capabilities": capabilityProvider,
})
if err != nil {
    return err
}
mux.Handle("/api/plugins/catalogs", authenticated(handler))
```

The GET/HEAD document has `discovery_version: 1` and a `catalogs` object with named,
independently versioned sections. Providers return JSON objects and must honor
request contexts and be concurrency-safe. Every request reads fresh sections;
there is no cross-module atomic snapshot. A missing section means unsupported.
Any section failure fails the entire response with a generic error, without
partial data or provider error text. Responses use `Cache-Control: no-store`.
Discovery is data, never a registration grant; no app adopts this endpoint here.

## Generated documentation sample

`Catalog.WriteMarkdown(io.Writer)` generates sorted reference documentation from
the same catalog document as discovery, including schemas and all policy fields.
The proposed [sample catalog](docs/catalog-sample.md) is generated with:

```sh
go run ./examples/catalog > docs/catalog-sample.md
```

The sample draws on the nine existing Nanite filter declarations in
`internal/plugin/filter.go` at source revision
`66ca11f4b12208df1f02dfa09d3187065fde9cb0`: `context_window`,
`assistant_response`, `envelope_data`, `system_prompt`, `user_message`,
`tool_result`, `tool_selection`, `reflex_state` and `reflex_action`.
Its dotted names and `value` envelopes are proposals, not aliases or an adapter.
Schemas deliberately illustrate only the envelope and value category; a host must
refine nested data schemas and supply compiled validators before installation.
The sample cannot be installed as-is and makes no remote transport claim. It does
not migrate Nanite, whose existing hooks and schemas stay unchanged.


## Observability and circuit breakers

`ExecutionConfig.Sink` accepts a dependency-free value-record interface for
completed dispatches, terminal handler attempts and generation breaker events.
Configure `Clock` and `Breaker` for deterministic cooldown testing or tuned
thresholds. Defaults are five consecutive failures and a 30-second cooldown;
one half-open probe runs per owner generation. Unavailable attempts follow the
existing error policy. Inspect/reset through `Engine.Breaker`, `Breakers` and
`ResetBreaker`; disposal is terminal and reset never releases stuck permits.

See [observability and breaker semantics](docs/observability.md) for exclusions,
trace context, timestamps, metrics aggregation, operator state and a host-side
OpenTelemetry blueprint. The [JSON sink example](examples/observability/main.go)
is standard-library-only. Records include no payloads, arbitrary metadata or
handler error text. No tracing backend, operator endpoint or app is adopted.

## TypeScript twin

The private browser/Node ESM package lives entirely under [ts/](ts/README.md).
Run `npm ci --ignore-scripts` and `npm run check` from that directory.
Go and vitest consume the same execution and registration-policy JSON fixtures
in `conformance/`; Go production code remains dependency-free. No npm publication
workflow or release tag is added.
