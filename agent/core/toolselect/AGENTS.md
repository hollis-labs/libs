# go-toolselect

Deterministic BM25 tool ranker and pure MCP tool-visibility profile evaluator.

It is not an authorizer, a config loader or a token-budget pruner. It decides what a caller sees; go-permission decides what it may call.

## Start Here

- `toolselect` (module root): `index.go` (`NewIndex`, `Rank`, BM25), `rule.go` (Rule/Match/Action), `tokenize.go` (tokenizer and stopwords), `options.go`, `tool.go` (types and tiers).
- `profile/`: `Evaluate` and its types. Standard library only, like the root.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Standard library only, no hollis libs (`go list -deps ./...` shows no non-stdlib import). `toolselect/launch` is deferred until `agent-contracts-leaf` is tagged; when it lands it must be its own package so the other two never depend on it. Do not add a stand-in `Assignment` type meanwhile.
- Ordering is a total order with the tier compared before the score, never folded into it (`TestTierExactNameOutranksStuffedDescription`, `TestRankIsDeterministicAndCatalogOrderIndependent`). Do not add a boost to the BM25 score.
- Equal-priority rules apply in declaration order, so the rule sort must stay `sort.SliceStable` (`TestEqualPriorityRulesApplyInDeclarationOrder`).
- Annotation hints are `*bool`; nil means undeclared and is never coerced (`TestAnnotationsPassThroughVerbatim`, `TestAnnotationPassthroughNilStaysNil`). With `Profile.ReadOnly`, a nil hint is hidden (`TestReadOnlyHidesNilHint`).
- Precedence in `Evaluate` is server disabled, deny, read_only, allow (`TestPrecedenceDenyReadOnlyAllow`). The zero Profile shows everything (`TestZeroProfileShowsEverything`); an unknown server id is `ErrUnknownServer` (`TestUnknownServerIsAnError`).
- A malformed glob in `Rank` rules matches nothing (`TestRuleValidationAndBadGlobs`, `FuzzRank`); in `Evaluate` it is `ErrBadPattern`.
- Do not port go-toolbroker's trailing-`*` HasPrefix fallback: `path.Match` already covers it and tool names contain no `/`.
- `AlwaysLoad` is informational and never moves a tool (`TestAlwaysLoadIsInformationalOnly`); only `Order` does. Per-field BM25F weighting is a deliberately open decision, not a gap to fill.
