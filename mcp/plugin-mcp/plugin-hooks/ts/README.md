# TypeScript plugin hooks

Private ESM twin of the Go registry and execution engine. The runtime has no
package dependencies or Node-only APIs. It targets ES2022 and recent browsers
with AbortController, structuredClone, TextEncoder and performance; Node 24 is
used for the development toolchain. This package is private and unreleased.

Hosts own identity, authorization, catalog publication, compiled JSON Schema
validators and transport. Give each plugin its generation-scoped `Scope`; keep
`Registry`, `Engine` and after-commit confirmation in the host.

```ts
import { Engine, Registry } from "@hollis-labs/plugin-hooks";

// catalogJSON is the Go CatalogDocument JSON, or the hooks section of discovery.
// compileSchema is the host's bounded synchronous schema compiler/validator.
const registry = new Registry(catalogJSON, (definition, schemas) => ({
  input: compileSchema(schemas.input),
  ...(definition.kind === "filter"
    ? { output: compileSchema(schemas.output!) }
    : {}),
}));
const engine = new Engine(registry);
const scope = registry.newScope({
  owner: "example",
  generation: "load-1",
  hooks: ["document.rendering"],
});
const handle = scope.addFilter(
  "document.rendering",
  "render",
  {},
  async (invocation) => {
    const value = JSON.parse(invocation.payload);
    // Only change catalog-declared mutable paths visible through this view.
    value.text = value.text.trim();
    return JSON.stringify(value);
  },
);
const result = await engine.applyFilters(
  "document.rendering",
  '{"text":" hello "}',
);
if (
  result.status === "success" ||
  result.status === "completed_with_open_errors"
) {
  render(result.value!);
}
scope.removeFilter(handle);
await scope.dispose(AbortSignal.timeout(1000));
await engine.shutdown(AbortSignal.timeout(1000));
```

The example's declaration, compiler and rendering function belong to the host;
it does not install implicit policies. Validators receive JSON text, return void
on success and throw on rejection. Async validators and non-void return values
are rejected. Catalog schemas are introspection data; the library never compiles
them. Compilers receive detached declarations plus lossless `schemas.input` and
`schemas.output` JSON text. Use the JSON text for schema numbers requiring exact
precision; the convenience schema objects use JavaScript numeric values.
`registry.catalog()` retains lossless schema JSON as well.

## Small API

- `Registry(catalogJSON, compileValidators)`, `newScope`, `catalog`, `remove`,
  `removeByPlugin`. Exactly one `Engine` attaches to a registry.
- `Scope.addAction(hook, registrationName, options, handler)` and `addFilter` return
  opaque handles. `remove`, `removeAction`, `removeFilter` are scoped and idempotent.
  `validateRegistration` preflights policy; `registrations` returns detached metadata.
- `Engine.doAction` and `applyFilters` return promises of structured results.
  `doActionAsync` and `applyFiltersAsync` are explicit promise API spellings with
  identical catalog policy. All handlers may be synchronous or asynchronous.
- `prepareAfterCommit` returns a one-shot host capability with `commit`/`rollback`.
  `Scope.dispose(signal?)` cancels starts/calls and waits for actual handler settlement.
  `Engine.shutdown(signal?)` closes admission, cancels dispatches and drains receipts.

Dispatch options carry `signal`, `metadata` (string values) and nested `context`.
An invocation includes its private JSON text, metadata copy, identity and a context
with an AbortSignal. Pass `invocation.context` to nested dispatches:

```ts
await engine.doAction("document.observed", invocation.payload, {
  context: invocation.context,
});
```

Explicit context propagation supports browsers without Node AsyncLocalStorage.
Dropping it bypasses the cooperative depth guard; in-process callbacks are trusted.

## Registration and execution policy

Omitted priority is 10; explicit 0 remains 0. Lower priorities run first, with stable
registration-sequence ties. Owner/generation pairs are permanently reserved, even
after disposal. Reload with a fresh generation. Names are unique across hooks
within a generation; stale or foreign handles remove nothing. Once is claimed
only after capacity is acquired and retired after the attempted call settles.
Cancellation while waiting for capacity does not consume once.

Names must exactly match catalog declarations. Custom names have exactly four
segments: `plugin.<owner_namespace>.<subject>.<event>`, with a host-assigned matching
namespace, schema and explicit resource/error policy. Namespacing grants no
permission; every scope needs an explicit hook allowlist. Remote scopes require
`remote_ok`. Required views cannot be bypassed. Deprecation warnings retain since,
replacement, reason and removal, and never redirect a name. Removing a declaration
makes registration fail with `unknown_hook`, including vendor lifecycle names.

Raw catalog decoding rejects unknown, duplicate and case-variant policy keys,
wrong types, non-finite limits and non-integral integer limit spellings. No aliases,
case conversion, numeric strings or silent option coercion are accepted. Schema
objects have their own unconstrained vocabulary. Payload duplicate object keys
follow Go's last-key-wins behavior; they are distinct from strict catalog policy.

Sequential actions and waterfall filters isolate each callback. Open failures
continue; closed failures stop sequential execution. Open filter failures retain
the previous accepted value; closed filters return no value. Throwing callbacks
are contained as `panic`; rejected handler promises are `handler_error`, unless
they carry a HookError classification. Bail mode treats `ErrCancelled` and
`ErrApprovalRequired` as terminal control outcomes even under open policy.
Parallel actions admit in priority order and return outcomes in registration order;
a closed failure cannot undo already admitted handlers.

`DispatchResult.status` is `success`, `completed_with_open_errors`, `failed_closed`,
`cancelled`, `approval_required`, `caller_cancelled`, or `queued`. Handler failures
are represented in `outcomes` and a terminal `error` where applicable. Dispatch
input/depth/admission failures reject with `HookError.code`. A queued receipt is
not success: `await result.future!.await(signal?)` reads a private result snapshot.

Timeout starts before capacity waits and uses the earliest handler, dispatch and
caller limit. Defaults are 64 active handlers, 8 per owner generation, depth 8,
256 queue slots and 4 async dispatch workers. Catalog `max_parallelism` is enforced
per dispatch. Timeout releases the caller but retains permits until the handler
actually settles. Waiting admission failure is `unavailable`; a started handler
that times out is `timeout`. Whole-dispatch cancellation overrides open policy.
Timers cannot preempt synchronous JavaScript that blocks the event loop; handlers
must yield and observe their signal. Removal/disposal invalidates late output.
Disposal wait can be bounded; a timed-out dispose remains terminal and can be
awaited again. Shutdown also cannot kill uncooperative callbacks.

Async mode uses bounded FIFO admission and visibly rejects overload as `queue_full`.
Work detaches from request cancellation after admission, retains nested depth and
counts queue wait against its dispatch budget. Workers can complete out of order.
After-commit preparation copies/validates immediately; transaction wait consumes
budget. Only the host calls `commit()` after confirmed commit. Rollback discards
work; commit/rollback consume the capability. This is in-memory scheduling without
retries, durable delivery or automatic transaction integration.

## JSON copies, masking and numeric precision

Payloads and filter results are JSON text. Handlers receive immutable strings and
private metadata. Internal lossless trees preserve numeric literals, including
integers beyond JavaScript's safe range. Views use JSON Pointer paths; hidden array
elements become null placeholders while positions and container shape remain
visible. Changes must be both mutable and visible. Merges preserve hidden fields;
array resizing requires permission and visibility of the whole array. The fully
merged result passes the host's output validator and payload byte limit.

For convenience the example uses JSON.parse/stringify; that can round large numbers
or change untouched literal text (`1.50` to `1.5`). As in Go, such a change on an
immutable path is rejected. Use a lossless JSON library in the handler, preserve
untouched text, or request a sufficiently narrow view. Numeric literal text is
part of the comparison semantics; no parser-normalization compatibility layer is
provided. Equality of result JSON objects does not require identical key order.

## Development and conformance

From this directory:

```sh
npm ci --ignore-scripts
npm run check
```

From the repository root:

```sh
go test . -run 'TestShared(Conformance|PolicyConformance)'
./scripts/check.sh
```

Both Go and vitest execute `conformance/execution.json` and `conformance/policy.json`.
The catalog inside the corpus uses the Go CatalogDocument contract with inline
schemas. Cases verify behavior, outcomes and literal-preserving merged JSON.
Additional TS tests cover capacity retention, simultaneous once claims, unload,
queue overload, cancellation, depth propagation and strict raw catalog decoding.
The built ESM smoke check executes a masking/filter example in a realm containing
browser globals and no Node core globals; it is not a browser UI automation test.
The pack check is dry-run only. No npm publication workflow is installed.

Breaker/telemetry seams, remote wire, schema compilation, UI adoption and durable
execution are future integrations. The twin does not include the Go breaker/sink.
