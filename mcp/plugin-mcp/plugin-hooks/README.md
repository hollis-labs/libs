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

All six declared modes execute. Breaker, tracing, remote wire, schema compiler,
TS parity, durable delivery and consumer adoption are unavailable. There are no
compatibility aliases, published repository or tags in this local implementation.

## Checks

Run fast checks with `GOWORK=off go test .`, `GOWORK=off go vet ./...` and
`golangci-lint run --allow-parallel-runners`. At final review run
`heavytest ./scripts/check.sh`; it matches CI checks and repeats concurrency-heavy
lifecycle tests with race instrumentation. Install local hooks with `lefthook install`
if desired; the tracked configuration alone installs nothing.
