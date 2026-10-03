# Changelog

## v0.1.0

### Added

- `hookstest`: adapter-based requirements R01-R15 for host dispatchers, explicit
  waivers with reasons, a reference Engine adapter and in-process plugin harness.
  The reference run has zero waivers; the full gate repeats it under the race
  detector. Covers ordering, errors, deadlines, lifecycle, depth, copying, views
  and catalog admission.

- Add explicit host catalog declarations and scoped action/filter registration.
- Resolve options, preserve stable priority ties and enforce unique names.
- Add opaque handles, atomic once claims and bounded generation disposal.
- Add lifecycle conformance tests and local/CI verification gates.
- Add sequential, parallel, bail, waterfall, async and after-commit execution.
- Bound handler capacity, deadlines, queue admission and nested dispatch depth.
- Contain panics, isolate JSON payloads and merge visible mutable filter changes.
- Return ordered outcomes, veto/approval statuses and async completion receipts.
- Add catalog JSON introspection and generated Markdown with inline schemas.
- Validate custom declaration namespaces and preflight manifest registrations.
- Retain structured deprecation warnings without redirects; name removal failures.
- Add one provider-based discovery endpoint for independently versioned catalogs.
- Document explicit lifecycle adapter mappings and proposed filter declarations.
- Add generation circuit breakers with single half-open probes and operator reset.
- Exclude control/caller/unload failures and retain permits across timeout/reset.
- Add injectable clocks, sanitized tracing/transition sinks and nested trace context.
- Validate filter output before lifecycle completion and breaker accounting.
- Add observability wiring examples and concurrent breaker/telemetry checks.
- Add the private TypeScript twin under `ts/`, with browser and Node execution,
  scoped lifecycle, field views and async variants; unpublished, with no npm
  publication workflow.
- Share execution and catalog-policy JSON conformance fixtures between Go and TS.
- Add the transport-agnostic `RemoteHandler` seam, `RemoteConnection` fences,
  structured result/veto validation, latency admission and generation breakers.
- Add `remote_latency_budget_ms`, `remote_batch_max` and
  `remote_fire_and_forget` catalog policies to Go introspection and the TS reader.
  Catalog document change: `remote_ok=true` requires a positive
  `remote_latency_budget_ms` within handler timeout; there is no independent
  document-format version.
- Add verified callback bindings with shared host-derived depth, inherited
  deadline/aggregate budget and trace, plus direct/indirect active-registration
  cycle rejection before scheduling.
- Add bounded observation-action batches with whole-envelope validation,
  private per-item leases, ordered independent outcomes, queued async execution
  and one-shot post-commit confirmation.
- Add opt-in async/post-commit notifications with queued-only submission outcomes,
  no callback binding, no retries and no fabricated handler/breaker success.
- Extend `hookstest` with R16-R22 and broken-adapter proofs for remote admission,
  results, fences, ancestry, batches, notifications and breakers; R01-R15 unchanged.
