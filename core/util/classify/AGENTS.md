# classify

Facet classification of short text: judges cast weighted votes, the engine combines them per facet with a real abstention, packs describe facets declaratively, and results map to decision-ledger rows.

It is not a model runtime, an embedding provider or a store. A change that adds a cgo dependency, a network call or a driver to this package is the wrong change: an `Embedder` or an LLM judge lives in its own module and implements `Judge` or `Embedder`.

## Start Here

- `classify.go` — `Phase`, `Input`, `Vote`, `Judge`, `Engine.Classify`, `combine`.
- `pack.go` — `Pack`, `Layer`, `Compile`, the rule judge.
- `bayes.go` — the hashed n-gram naive Bayes judge. `centroid.go` — the embedding centroid judge and `Embedder`.
- `eval.go` — `Evaluate` and `Report`. `calibrate.go` — `FitTemperature`. `ledger.go` — `Result.LedgerRecords`.
- `decision.go` — the judge that asks a `decide.Decider` (JEV through a gateway). `affect.go` — the tone lexicon judge.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- Standard library plus `core/util/ledger` and `core/util/decide` only.
- Text sent to a decider is scrubbed first, and option order is rotated deterministically by the text. Guarded by `TestDecisionJudgeAsksThePackAndVotesItsAnswer`.
- A judge votes; only the engine decides. A failing judge loses its votes and is named in `Result.Failed`; it never fails a classification. Guarded by `TestFailingJudgeIsRecordedNotFatal`.
- Abstention is real: no votes, a top label under `min_p` (or its label's own, higher `min_p`), or a margin under `min_margin` gives `Label == ""`. Guarded by `TestRulesVoteAndCombine` and `TestRiskyLabelNeedsMore`.
- Two decisive votes for different labels do not settle a facet. Guarded by `TestDecisiveSettlesAndConflictingDecisivesCancel`.
- A judge never runs below its phase. Guarded by `TestPhaseGatesJudges`.
- Votes for an unknown facet or label, or with a non-finite weight, are dropped. Guarded by `TestUnknownFacetsLabelsAndNaNAreIgnored`.
- The pack hash tracks content, not the version label; the engine fingerprint follows a learning judge. Guarded by `TestHashTracksContentNotVersion` and `TestEngineFingerprintFollowsLearning`.
- One ledger row per facet and phase; a replay of the same engine over the same input is a duplicate. Guarded by `TestLedgerRecordsOnePerFacetAndReplayIsIdempotent`.
