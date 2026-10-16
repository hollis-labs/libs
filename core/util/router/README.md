# router

A small declarative matcher: it maps the fields of a subject to a target, applies a real confidence gate, asks a policy hook, and returns a decision shaped to be one row of a decision ledger.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

See the module's `CHANGELOG.md` for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/libs/core/util/router
```

## Usage

```go
set, _ := router.ParseRuleSet([]byte(`{
  "version": "2026-10-16.1",
  "strategy": "first_match",
  "rules": [
    {"id": "save-note", "priority": 10,
     "when": [{"field": "kind", "op": "eq", "value": "note"}, {"field": "action", "op": "eq", "value": "save"}],
     "target": "notes.write", "auto": true, "min_confidence": 0.8}
  ]}`))
r, _ := router.Compile(set, router.Config{})

conf := 0.4
d, _ := r.Route(ctx, router.Subject{
	ID:         "msg-1",
	Fields:     map[string]string{"kind": "note", "action": "save"},
	Confidence: &conf,
})
fmt.Println(d.Outcome, d.Matches[0].Disposition) // suggested ask
```

## Rules

A rule set has a `version`, an explicit `strategy` (`first_match` or `fan_out`; no default) and rules. A rule is conditions on flat dotted field names (`eq`, `not_eq`, `in`, `prefix`, `glob`, `exists`), a `target`, an optional `priority` (higher first), and optional gating:

- `auto: false` means a match is only a suggestion (`ask`).
- `min_confidence` is a real gate. A subject confidence below it, or **unknown**, is not a pass: `on_low_confidence` (`ask` by default, `deny` or `abstain`) decides. Unknown is not treated as zero, and the reason says which it was.
- `glob` uses `*` for any run of characters, **including `/`**, and `?` for one character, so URL and path rules work (`path.Match` stops at `/`).

`Compile` rejects unknown operators, empty conditions, duplicate ids, a missing strategy, and confidence outside 0 to 1, and fingerprints the content (`Router.Hash`).

## Policy and escalation

With `Config.Policy` set, each match is evaluated and the most restrictive of rule and policy wins. A verdict can tighten a match, never loosen it; a policy error denies. Obligations are carried on the match: `approval` makes it `ask`, `dry_run_only` sets `DryRun`, `rate_limit` travels with its limit (a malformed one denies), and an unknown kind denies. With `Config.PolicyShadow` verdicts are recorded and not applied. When nothing routed, `Config.Escalator` can hand the subject to a later stage.

The adapter for the policy module is `security/routepolicy`; this package does not import the policy module.

## The decision

`Decision` keeps the input (shaped by `Config.Snapshot`, which is where redaction belongs), the rule-set version and hash, every rule considered and why it did or did not match, matches with dispositions, obligations and policy provenance, and shadow verdicts. `Decision.LedgerRecord` converts it to a `ledger.Record` (replaying the same rules over the same subject yields the same idempotency key).

## Limits

- Matching is by equality, prefix, membership and glob on strings. There is no expression language.
- The router holds no state and counts nothing; rate limits and approvals are the host's to fulfill.
- `Decision.Input` snapshots whatever the subject holds unless `Config.Snapshot` narrows it.
