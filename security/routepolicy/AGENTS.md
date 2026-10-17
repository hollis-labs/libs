# routepolicy

The adapter from `policy.Evaluator` to the router's `Policy` seam.

It is not a policy engine and adds no evaluation rules, modes or audit of its own. A change that re-implements shadow, fail-open or auditing here is the wrong change; the evaluator owns those.

## Start Here

- `routepolicy.go` — `Adapter`, `Mapper`, `DefaultMapper`, the translation back to a router verdict.

## Commands

```sh
export GOWORK=off
scripts/check -race security/routepolicy
```

## Boundaries

- Its own module: `core/util/router` cannot import `security`, and `security/policy` must stay free of adapters and of dependencies on other modules (its `doc.go` says so). This module imports both.
- The verdict effect is the evaluator's admission, not the raw decision. Guarded by `TestEffectsMapThroughRouter` and `TestPolicyShadowModeAppliesButRecordsWouldBlock`.
- Error text from the PDP, the sink or the mapper never enters a verdict. Guarded by `TestFailuresFailClosedWithoutLeakingText`.
- A missing principal, an invalid request, an invalid decision and a failed audit all deny. Guarded by `TestMappingFailureAndInvalidRequestDeny`, `TestUnknownObligationKindDenies` and `TestAuditUnavailableDenies`.
- Obligations flow to the router unchanged. Guarded by `TestObligationsFlowToRouterRules`.
