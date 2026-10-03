# go-modelsdev-catalog-helpers

Cost calculation over disjoint LLM usage components: go-usage-ledger Usage priced with go-modelsdev Pricing.

`costcalc.Price` multiplies the five disjoint components of a `usageledger.Usage` (uncached input, cache read, cache write, output, reasoning) by the five rates of a `modelsdev.Pricing`, each at its own rate, and returns a `Cost` whose `Total()` is derived from the per-component dollar amounts rather than stored. A model missing from the catalog is a `Cost` with `Priced == false`, not an error.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/llm-core/costcalc
```

Requires Go 1.26.6 or newer. Depends on [go-usage-ledger](https://github.com/hollis-labs/go-usage-ledger) v0.1.0 and [go-modelsdev](https://github.com/hollis-labs/go-modelsdev) v0.2.0 (which pulls in `cenkalti/backoff/v5`).

## Usage

```go
package main

import (
	"fmt"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func main() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}
	// ReasoningTokens is left untouched: the provider did not report it.

	pricing := modelsdev.Pricing{Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3}
	cost := costcalc.Price(u, costcalc.PriceSnapshotFromPricing(pricing))

	fmt.Printf("$%.6f priced=%t provenance=%s\n", cost.Total(), cost.Priced, cost.Provenance)
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). `PriceModel(cat, providerID, modelID, usage)` does the same after looking the model up in a `Catalog` (a `*modelsdev.Client` satisfies it), and `PriceRow(row)` prices a recorded `usageledger.Row` at the `Price` snapshot stored on it. Take the snapshot for a new row with `SnapshotPrice`.

Read `Cost.Priced` before treating a zero total as free, and read `Cost.Provenance` before treating the total as exact: it is `usage.TotalProvenance()`, and an unreported component contributes $0.

## Why

Five places in the Hollis Labs apps price only some of the five components. Nanite's `estimateCost` (called from `RecordUsage` and `RecordExecutionMetrics`) prices input and output only, from an app-local table rather than `go-modelsdev`. Tesseract's `computeCost` reads `Cost.Input` and `Cost.Output` from a full `modelsdev.Pricing`. Torque folds cache tokens into the prompt count in `executor-api/anthropic.go`, and its `modelcatalog.Catalog.Pricing` and `EstimateCost` then price that count at the input rate and read no cache or reasoning rate; `Pricing` also reports "not found" for a found model whose input and output prices are both zero. `TestPrice_AllFiveComponentsPriced`, `TestPrice_NaiveShapesUnderstate` and `TestPriceModel_ZeroPriceIsPricedNotUnpriced` are the regression tests: local re-creations of the input/output-only shape and the fold-then-price shape fail them, this package passes.

## Provenance of this code

The design follows decision D-27 of the agent-fabric vNext register and the brief for this library. Nothing was lifted from the apps; their source was read (not run) at the paths the brief cited, and the naive shapes in the tests are local re-creations of what was read. No behavioral equivalence with any application is claimed. `go-usage-ledger` and `go-modelsdev` are consumed read-only at their tagged versions. Nothing uses this module yet.

## Compatibility

This module is pre-1.0 and unreleased: any release, including a minor one, may break the exported API, and there is no deprecation period. Pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Known limitations

- Costs are unrounded `float64` USD. Rounding and display precision are the caller's concern.
- A zero rate contributes $0. The catalog's pricing type cannot tell "free" from "rate not populated" for the optional cache and reasoning rates, and neither can this package.
- `Priced == false` covers both "model not in the catalog" and "the catalog's cache was never populated": `Catalog.Get` does not distinguish them. A caller that cares should check `modelsdev.Client.LastFetchedAt()` before pricing.
- `Cost.Provenance` includes `Dims` entries even though they are never priced, so an uncertain extension dimension can make a fully priced total read `estimated` or `unknown`.
- `Price` does not call `Usage.Validate`, and negative or non-finite rates or token counts are not rejected; garbage in gives garbage out (`FuzzPrice` covers the non-negative, finite, bounded domain).
- `Total()` is a lower bound whenever `Provenance` is not `measured`.

## Out of scope

- Persistence: no store, schema or migrations. This package returns `Cost` values and writes them nowhere.
- Currency conversion or multiple currencies: everything is USD, as in the catalog.
- Pricing `Usage.Dims` entries: `PriceSnapshot` has no rate for an arbitrary dimension.
- Re-pricing a `Row` from a live catalog instead of its stored snapshot.
- Fetching, caching or refreshing the catalog: the caller owns the `Catalog` (in practice a `*modelsdev.Client`).
- Changes to `go-usage-ledger` or `go-modelsdev`, and adoption in any application.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
