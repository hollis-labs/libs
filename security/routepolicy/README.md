# routepolicy

Adapts a `policy.Evaluator` to the router's `Policy` seam (`core/util/router`), so each routing candidate is checked by the policy decision point and its audit event is written.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

See the module's `CHANGELOG.md` for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/libs/security/routepolicy
```

## Usage

```go
r, _ := router.Compile(ruleSet, router.Config{Policy: routepolicy.Adapter{
	Evaluator: policy.Evaluator{PDP: myPDP, Audit: myAuditSink},
	Options:   policy.Options{Mode: policy.Enforce, FailMode: policy.FailClosed},
	Map:       routepolicy.DefaultMapper("my/routing-scope"),
}})
```

## What it does

- A `Mapper` projects a routing candidate to a `policy.Request`. `DefaultMapper` takes the principal from the subject's `principal.urn` and `principal.kind` fields, uses the action `route:<target>` and the target as the resource, and passes the rule id, the subject's fields and its confidence as context. A subject with no principal is refused.
- The evaluator keeps its own enforce/shadow modes, fail-closed or fail-open-with-audit, and its audit event. The verdict's effect is the evaluator's **effective admission**; `WouldBlock` is carried when the evaluator was in shadow mode.
- Obligations (approval, dry-run, rate limit) and matched-policy provenance come through unchanged, and the router applies its obligation rules to them.
- A failure comes back as a fixed code (`pdp_failed`, `invalid_request`, `invalid_decision`, `audit_unavailable`, ...) on the match, and never as error text, which can carry private input. A failed audit write tightens admission to deny in every mode.

## Limits

- The default mapper passes every subject field to the policy decision point. Route untrusted subjects with your own `Mapper` that chooses what reaches it.
- This is its own module on purpose. `security/policy` stays a neutral contract with no adapters and no dependencies on other modules, and the router lives in `core/util`, so the adapter sits beside both and is released after both.
