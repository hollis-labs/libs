# router

A declarative matcher with a confidence gate, a policy hook and an escalation hook, producing a decision shaped for a ledger.

It is not a workflow engine, a rules language or a policy engine. A change that adds loops, variables or expression evaluation to rules, or makes the router execute a target, is the wrong change.

## Start Here

- `router.go` — `RuleSet`, `Compile`, `Router.Route`, `Decision`, `applyVerdict`, the policy and escalation seams.
- `ledger.go` — `Decision.LedgerRecord`, the mapping to a ledger row.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- No import of the policy module (core may not import security). The adapter is `security/routepolicy`.
- `strategy` has no default. Guarded by `TestCompileRejects`.
- A confidence gate must fire, and unknown confidence is not zero and not a pass. Guarded by `TestConfidenceGateIsReal`.
- A policy verdict can tighten a match and never loosen it; a policy error denies. Guarded by `TestPolicyMostRestrictiveWins`.
- An unknown or malformed obligation denies, approval asks, dry-run is carried. Guarded by `TestObligationsAreNotJustAllow`.
- `glob` crosses `/`. Do not replace the matcher with `path.Match`. Guarded by `TestGlobCrossesSlashes`.
- The stored input goes through `Config.Snapshot`. Guarded by `TestSnapshotHookRedactsStoredInput`.
- The rule-set hash tracks content, not the version label. Guarded by `TestHashTracksContentNotLabel`.
- A replay of the same rules and subject gives the same ledger idempotency key; changed rules give a new one. Guarded by `TestDecisionLandsInLedger`.
