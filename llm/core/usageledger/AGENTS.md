# go-usage-ledger

Disjoint LLM token-usage ledger: fixed core plus Dims, per-component provenance, derived totals.

It is not a cost calculator, a store, or an adapter for any provider's response type: it defines the data shape of one usage record and derives a total from it. Do not add pricing math, storage, provider conversions or dependencies.

## Start Here

- `usageledger` package (module root) — the importable API; `doc.go` is the package documentation, `usageledger.go` holds every type.
- `usageledger_test.go` — behavior and regression tests; `fuzz_test.go` — `FuzzValidate`.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it, and `Example_quickstart` mirrors it.
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
- Standard library only, no hollis libs (no `go-modelsdev`, no `go-llm-types`): `go list -deps ./...` shows nothing outside the stdlib and this module.
- The total is derived, never stored. Do not add a total field or column-shaped helper (`TestUsage_TotalTokens_SumsAllFiveAndDims`).
- Unreported is not zero. `Provenance` has no valid zero value; `NewUsage` defaults every core component to unknown; a bare `Usage{}` fails validation and never reports a measured total (`TestUsage_Validate_RejectsBareLiteral`, `TestUsage_UnreportedIsNotMeasuredZero`, `TestUsage_TotalProvenance_WorstOf`).
- Unknown implies 0 tokens, and non-zero tokens imply not unknown (`TestUsage_Validate_RejectsUnknownWithNonzeroTokens`).
- A `Dims` key may not equal a core JSON tag, case-insensitively, or `TotalTokens` would double-count (`TestUsage_Validate_RejectsDimsCollision`). `FuzzValidate` must never panic.
- The five core fields map one to one to a model catalog's five rates. A sixth core field is a design decision (D-27), not a convenience; extras go in `Dims`.
- `Row` has no cost field and `Price` is only ever set by the caller (`TestRow_PriceNilByDefault`). `PriceSnapshot` is inert data; never multiply it against a `Usage` here.
- `Row.CostKind` is a label, not a cost; `IsBill` is true only for `api_billed` (`TestCostKind_ValidAndIsBill`). `PriceSnapshot` stays comparable with `==`, which is why `UnknownRates` is a struct of bools, not a slice or map (`TestPriceSnapshot_Comparable`).
- JSON tags are the wire format; `omitempty` on `Dims`, `Price` and `MessageID` is pinned by `TestRow_JSONRoundTrip`, and the cost-kind and snapshot provenance fields are omitted when empty so a legacy row re-encodes byte for byte (`TestRow_LegacyJSONDecodesAndReencodesUnchanged`). `UnknownRates` JSON tags equal the core `Usage` tags. Do not hand-roll marshalers.
- Behavioral equivalence with any application is not claimed: nothing was ported. The app code was read, not run.
- Out of scope: cost and pricing math, persistence, provider adapters, adoption in an application.
