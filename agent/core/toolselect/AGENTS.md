# go-toolselect

Deterministic BM25 tool ranker and pure MCP tool-visibility profile evaluator.

It is not an authorizer, a config loader or a token-budget pruner. It decides what a caller sees; go-permission decides what it may call.

## Start Here

- `toolselect`: `index.go` (`NewIndex`, `Rank`, BM25), `rule.go` (Rule/Match/Action), `tokenize.go` (tokenizer and stopwords), `options.go`, `tool.go` (types and tiers).
- `profile/`: `Evaluate` and its types. Standard library only, like the root.
- `launch/`: `FromAssignment`, the only package that imports
  `substrate/llm-core/contracts`.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.
- The repository-root workflows run the whole `agent` module gate.

## Commands

```sh
scripts/check agent
```

Run from the repository root. CI uses the same module check.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- `toolselect` and `toolselect/profile` are standard library only; only
  `toolselect/launch` may import `substrate/llm-core/contracts`, and nothing
  else may. Do not add a stand-in `Assignment` type.
- `launch` is a ceiling, never an expansion (`TestNeverWidensBase`). An empty intersection must stay a non-empty allow list holding the never-matching entry, because an empty `ToolsAllow` allows everything (`TestEmptyIntersectionNeverBecomesEmptyAllowList`). Globs are intersected only via literals or identity; do not "improve" that with prefix heuristics (`path.Match("a_?*", "a_*")` is true but `a_*` is not inside `a_?*`). Grant entries are tool-name globs, not server ids.
- Ordering is a total order with the tier compared before the score, never folded into it (`TestTierExactNameOutranksStuffedDescription`, `TestRankIsDeterministicAndCatalogOrderIndependent`). Do not add a boost to the BM25 score.
- Equal-priority rules apply in declaration order, so the rule sort must stay `sort.SliceStable` (`TestEqualPriorityRulesApplyInDeclarationOrder`).
- Annotation hints are `*bool`; nil means undeclared and is never coerced (`TestAnnotationsPassThroughVerbatim`, `TestAnnotationPassthroughNilStaysNil`). With `Profile.ReadOnly`, a nil hint is hidden (`TestReadOnlyHidesNilHint`).
- Precedence in `Evaluate` is server disabled, deny, read_only, allow (`TestPrecedenceDenyReadOnlyAllow`). The zero Profile shows everything (`TestZeroProfileShowsEverything`); an unknown server id is `ErrUnknownServer` (`TestUnknownServerIsAnError`).
- A malformed glob in `Rank` rules matches nothing (`TestRuleValidationAndBadGlobs`, `FuzzRank`); in `Evaluate` it is `ErrBadPattern`.
- Do not port go-toolbroker's trailing-`*` HasPrefix fallback: `path.Match` already covers it and tool names contain no `/`.
- `AlwaysLoad` is informational and never moves a tool (`TestAlwaysLoadIsInformationalOnly`); only `Order` does. Per-field BM25F weighting is a deliberately open decision, not a gap to fill.
