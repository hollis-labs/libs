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

## Current boundary

This registry-only revision has no public dispatch API and advertises no executable
modes. Internal snapshot/start/finish lifecycle machinery is exercised by unit tests.
Execution modes, payload isolation/views, deadlines, panic containment, bounded
capacity, depth and async/after-commit are the next implementation task. Catalog
schema generation, wire protocol, breaker, tracing and TS parity are separate work.
No consumer adoption, compatibility aliases, published repository or tag is included.

## Checks

Run fast checks with `GOWORK=off go test .`, `GOWORK=off go vet ./...` and
`golangci-lint run --allow-parallel-runners`. At final review run
`heavytest ./scripts/check.sh`; it matches CI checks and repeats concurrency-heavy
lifecycle tests with race instrumentation. Install local hooks with `lefthook install`
if desired; the tracked configuration alone installs nothing.
