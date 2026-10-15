# agent-contracts-leaf

Zero-dependency agent contracts: Assignment, RunPolicy, LaunchRecord, and the capabilities and runtime vocabularies.

It is not a launcher, an enforcer or a store, and it is not the agent definition format: it is pure data that hosts share so they stop each inventing "what does it take to launch an agent". A change here that adds behaviour, a dependency or a host's taxonomy is almost certainly the wrong change.

## Start Here

- `assignment.go` — `Assignment` and its `Validate`; the shared launch contract. Orchestration (hooks, escalation, gates, dependencies) stays out of it.
- `runpolicy.go` — `RunPolicy` is `{lifetime, attach, attended, resume}`. There is no mode field.
- `launchrecord.go` — `LaunchRecord`, the one noun for "what was actually launched", with four digests.
- `trust.go` — `EffectiveTrust`, which can only lower.
- `capabilities/` — the capability vocabulary and `Check`; the root package imports it, never the reverse.
- `runtimes/` — the canonical runtime `ID`, `Mode` and runtime `Capability` vocabulary (D-73). Words only: which runtime has which mode or capability is the go-providers registry's, not this package's.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- Standard library only. `go list -deps ./...` must show nothing else, and no hollis-labs lib may be imported (go-permission, go-materialize, agentkit, go-agentdef all depend on this direction, not the reverse).
- Clean break: no aliases, shims or `Deprecated:` markers (D-22/D-25). The legacy launch mode words have no compatibility constants; `TestNoRuledOutFields` guards them.
- `capabilities.Check` takes no force flag, and `EffectiveTrust` has no parameter that could raise its result. Overriding an unmet `requires` is the caller's decision, recorded in `LaunchRecord.Forced` — capabilities only, never grants, auth, posture or trust. `TestCheckTakesNoForceFlag` and `TestEffectiveTrustUnknownFailsSafe` guard it.
- `Grants` is a ceiling, not an enforcement claim: a grant a host does not enforce is a `GrantDiagnostic` with `Enforced` false, never silence.
- `runtimes` enums are closed and carry no legacy spellings (`app-server`, `serve-http`, `subprocess`, `claude-print`) and no runtime aliases (`claude-code` belongs to the go-providers registry); `TestEnumsRejectOutsiders` in `runtimes/` guards it. Permission posture is not a runtime word: it reuses go-permission's `Mode` (D-72).
- `Requester` and `Correlation` are opaque couriers; never interpret or authorize on them.
- JSON tags are the wire contract; `TestAssignmentJSONGolden` pins the spellings. YAML tags are present but untested here so the module stays stdlib-only.
