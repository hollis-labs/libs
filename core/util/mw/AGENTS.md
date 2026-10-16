# mw

One middleware contract with a descriptor checked at assembly, plus adapters for HTTP (`httpmw`) and queue jobs (`jobmw`).

It is not an HTTP framework, a router, an auth system or a queue. A change that makes `mw` import an auth stack, an otel setup or an app-specific gate is the wrong change; those plug in as members.

## Start Here

- `descriptor.go` — `Descriptor`, `Phase`, `FailureMode`, `Mode`, `Validate`, `Explain`, `Fault`. The assembly rules.
- `mw.go` — the generic `Handler`/`Middleware`/`Chain`, `Stack`, the guard that applies failure modes, shadow mode, `RecoverMember`, `TimeoutMember`.
- `httpmw/`, `jobmw/` — adapters. `conformance/` — the scenario table every adapter runs.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- Standard library only, plus `svcerr` for the HTTP error body. No otel, no auth, no policy: those are members written elsewhere.
- Descriptors are checked once, in `Assemble`; nothing on the per-call path re-validates. Guarded by `TestValidateRejects`.
- A failure mode never blames a member for a panic raised below it. Guarded by `TestDownstreamPanicIsNotBlamedOnOpenMember`.
- A `Fault` is owned by the guard that handled it; an outer member passes it through. Guarded by the conformance scenarios for closed members.
- Shadow mode never applies a result and never fails the request, even when the shadowed member breaks. Guarded by `TestShadowRecordsButDoesNotApply` and the conformance shadow scenarios.
- A new adapter is not done until it passes `conformance.Run`. A behavior that differs between adapters is a bug in an adapter, or a scenario that is missing.
- HTTP error bodies go through `svcerr`; a message is never derived from an internal error.
