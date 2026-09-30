# go-modelsdev-catalog-helpers

Cost calculation over disjoint LLM usage components: go-usage-ledger Usage priced with go-modelsdev Pricing.

It is not a catalog client, a store, or a currency library: it multiplies a `usageledger.Usage` by a price and returns a `Cost`. Do not add fetching, persistence, rounding or currency, and do not fold cache tokens into the input count before pricing.

## Start Here

- `costcalc` package (module root) — the importable API; `doc.go` is the package documentation, `costcalc.go` holds every function.
- `costcalc_test.go` — behavior and regression tests; `fuzz_test.go` — `FuzzPrice`.
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

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either. Depend on tagged versions of `go-usage-ledger` and `go-modelsdev`, never a local path or an unpushed pseudo-version.
- Deliberate reversal of `go-usage-ledger`'s leaf stance: this module imports both `go-usage-ledger` and `go-modelsdev`, because it is math over both types. `go list -deps ./...` must show only those two, `cenkalti/backoff/v5` and the stdlib. Do not define local copies of `Usage` or `PriceSnapshot`.
- All five components are priced, each at its own rate. Never fold cache or reasoning tokens into input, and never read only input and output (`TestPrice_AllFiveComponentsPriced`, `TestPrice_EachComponentUsesItsOwnRate`, `TestPrice_NaiveShapesUnderstate`).
- The total is derived, never stored: `Cost` has no total field (`TestPrice_TotalIsDerived`).
- Found is not the same as priced-nonzero. "Not in the catalog" comes from `Catalog.Get`'s own bool, never from a zero-price check; a free model is `Priced: true` with total 0 (`TestPriceModel_ZeroPriceIsPricedNotUnpriced`, `TestPriceModel_ModelNotFound`, `TestSnapshotPrice_Found`).
- An uncataloged model is data (`Cost{Priced: false}`), never an error or panic. `Provenance` is taken from the usage alone, independent of price availability.
- `Cost.Provenance` is `Usage.TotalProvenance()` unchanged, including `Dims`; do not special-case it (`TestPrice_ProvenancePropagates`, `TestPrice_DimsUncertaintyStillReachesProvenance`).
- `Dims` are never priced (`TestPrice_DimsNotPriced`).
- `PriceRow` uses the row's stored snapshot only. Do not add a variant that re-prices from a live catalog (`TestPriceRow_UsesStoredSnapshotNotLiveCatalog`, `TestPriceRow_NilPriceIsUnpriced`).
- `PriceSnapshotFromPricing` must track `modelsdev.Pricing` field for field; a new pricing dimension on either side fails `TestPriceSnapshotFromPricing_FieldParity`. This is the conformance test `go-usage-ledger` deferred to this module.
- Plain `float64` USD, no rounding, no currency. `FuzzPrice` must never panic or return a non-finite total on its bounded domain.
- Behavioral equivalence with any application is not claimed: nothing was ported and the app code was read, not run.
- Out of scope: persistence, currency, `Dims` pricing, catalog lifecycle (`Refresh`, `StartRefresher`, constructing a client), adoption in an application.
