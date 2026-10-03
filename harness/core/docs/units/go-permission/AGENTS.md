# go-permission

Tool-invocation authorization core: allow/deny/ask decisions, rules, approvals and path grants.

It is not: a sandbox, a command denylist, a transport, or a tool-offering (grant-membership) gate. It answers allow/deny/ask for one call and runs the approval handshake; enforcement and confinement belong to the host.

## Start Here

- `engine.go` — `Engine`, `NewEngine`, options, and `Check` (the precedence order lives here).
- `approval.go` — `RequestApproval` / `WaitForApproval` / `Respond`. Isolated in its own file so it can be moved out later without touching the rest.
- `rules.go` — `Rule`, `RuleSet` (`Evaluate`, `Validate`), `Matcher`, glob matching, YAML load/save/merge.
- `resolve.go`, `derivation.go` — `./` pattern anchoring and parent-to-subagent rule derivation.
- `audit.go`, `rulestore.go` — the `Auditor` and `RuleStore` seams the host implements.
- `pathgrants/` and `summary/` — sub-packages that import the root. **The root must never import them** (import cycle; `go build ./...` catches it).
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
go test -race -count=20 -run TestConcurrentEngine .
go test -run '^$' -fuzz FuzzMatchPathGlob -fuzztime 10s .
go test -run '^$' -fuzz FuzzExtractPathMentions -fuzztime 10s ./pathgrants
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Only stdlib and `gopkg.in/yaml.v3`. Do not add other dependencies, and do not import go-agent-wrapper, go-toolbroker or go-runtime-events.
- Check precedence is `yolo > plan > session grant > deny rules > ask rules > allow rules > mode default`, pinned by `TestPrecedence_*` in `precedence_test.go`. Yolo beating deny rules, and session grants (keyed by tool NAME only) beating deny rules, are deliberate; do not "fix" them without a decision. There is no option to change the order.
- The approval timeout default is 5 minutes (`DefaultApprovalTimeout`); the rationale is on that constant. Do not shorten it to 60s. Headless consumers set `WithApprovalTimeout`.
- `Respond` must reject any session id that is not exactly the request's (an empty id is not a wildcard): `TestEngineRespondWrongSession`, `TestEngineRespondEmptySessionRejected`. A request can be answered once; a second `Respond` must not record a grant: `TestEngineRespondTwice`.
- `/**` matches on a path-segment boundary (`/work/proj/**` must not match `/work/proj-secret/x`): `TestPathGlobSegmentBoundary`, `TestRuleDoesNotOvermatchSiblingDir`. A malformed tool glob matches nothing (`TestMatchGlob`); `Validate` is how it gets reported.
- `Evaluate` returns a copy of the matched `Rule`, never a pointer into the `RuleSet`: `TestEvaluateReturnsCopy`.
- Auditor calls happen outside every engine lock and events never carry tool input (it may hold secrets): `TestAuditor_EveryKindFiredOnce`. `Actor`/`OnBehalfOf` on `Event` are reserved and stay empty.
- `Matcher` limits are documented, not bugs: a `Pattern` only sees the configured input keys, and default command matching is advisory substring. Do not widen the default keys or "improve" substring matching silently; keep the README sharp-edges note true.
- The default file-edit tool set is empty. Host tool names belong in `WithFileEditTools` or `ToolMeta.IsFileEdit`, not in this repo. Nothing exported may be named after a consuming application.
- The `Auditor` vocabulary is separate from go-agent-wrapper's advisory `policy` package (observe/nudge/rewrite/block/approval). Do not merge or map them.
- `ScopeProject` persistence is the host's job through `RuleStore`; do not add a file-backed implementation here.
- `pathgrants` and `summary` keep their own `doc.go`. Comments must stay generic: no application names, and no task-tracker citations.
