# pricesource

Price sources for `costcalc`: a models.dev adapter, override tables with explicit unknown rates, a LiteLLM-style table parser, a chain, and an offline file cache.

`costcalc.PriceSource` is one method, `Snapshot(providerID, modelID) (usageledger.PriceSnapshot, bool)`. This package implements it. Every snapshot it returns carries `Source` and `AsOf` as far as the source knows them, and marks in `UnknownRates` each rate the source does not have, so `costcalc.Price` reports the cost as partial instead of pricing the missing component at $0. A source never multiplies rates against usage: `costcalc` does that.

## Status

**Pre-release.** This package is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/llm-core/pricesource
```

Requires Go 1.26.6 or newer. Depends on `llm-core/costcalc`, `llm-core/usageledger`, `llm-core/modelsdev` and the standard library.

## Usage

```go
package main

import (
	"fmt"
	"log"
	"time"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/pricesource"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func main() {
	asOf := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// An override table: the cache-write rate for this model is not known,
	// so it is left nil rather than written as 0.
	overrides, err := pricesource.NewTable("overrides", asOf, pricesource.Entry{
		Provider:  "anthropic",
		Model:     "claude-example",
		Input:     pricesource.Rate(3),
		Output:    pricesource.Rate(15),
		CacheRead: pricesource.Rate(0.3),
	})
	if err != nil {
		log.Fatal(err)
	}

	// Overrides win; a models.dev source (pricesource.FromModelsDev) would
	// normally follow as the fallback.
	src := pricesource.Chain(overrides)

	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}

	cost := costcalc.PriceFromSource(src, "anthropic", "claude-example", u)
	cost.Kind = usageledger.CostKindAPIEstimated

	fmt.Printf("$%.6f priced=%t partial=%t unpriced_tokens=%d kind=%s bill=%t\n",
		cost.Total(), cost.Priced, cost.Partial(), cost.UnpricedTokens, cost.Kind, cost.IsBill())
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). It prints `$0.010500 priced=true partial=true unpriced_tokens=500 kind=api_estimated bill=false`: the 500 cache-write tokens have no rate, so the total is a lower bound, and an estimate is never a bill.

The sources:

- `FromModelsDev(client)` / `Catalog{...}` adapts any `costcalc.Catalog`. Snapshots carry `Source: "models.dev"` and, for a `*modelsdev.Client`, `AsOf` from `LastFetchedAt()`.
- `NewTable(name, asOf, entries...)` builds an immutable table. An `Entry` rate is a `*float64`: `nil` is unknown, `Rate(0)` is free. `Provider: AnyProvider` (`"*"`) matches the model under any provider; an exact provider match wins.
- `FromCatalog(name, asOf, models)` freezes a list of catalog models into a table.
- `ParseLiteLLM(r, name, asOf)` reads a LiteLLM-style JSON price map (per-token `input_cost_per_token`, `output_cost_per_token`, `cache_read_input_token_cost`, `cache_creation_input_token_cost`, `output_cost_per_reasoning_token`), converts to per-million rates, strips a `provider/` key prefix, and returns the keys it skipped. A missing rate is unknown.
- `Chain(sources...)` returns the first source that has the model.
- `Live` holds a source behind an atomic swap, for a refresher that replaces a table without locking readers.
- `ParseTable`, `(*Table).WriteJSON`, `LoadFile`, `SaveFile` (atomic rename) and `FileCache` keep a table on disk. `FileCache.Refresh(ctx, fetch)` saves a fresh fetch and falls back to the saved copy when the fetch fails.

## Cost kinds

`usageledger.Row.CostKind` labels what a figure means:

| Kind | Meaning | `IsBill()` |
|---|---|---|
| `api_billed` | the provider charges this amount for API usage | true |
| `api_estimated` | an estimate of API charges (rates looked up, not invoiced) | false |
| `subscription_equivalent` | what the usage would cost at API rates, under a flat subscription | false |
| `local_compute` | a model run on owned hardware | false |
| `""` (unspecified) | rows written before cost kinds existed | false |

Present a figure as a bill only when `Cost.IsBill()` is true. A subscription-equivalent figure is never a bill.

## Migration

The ledger schema change is backwards compatible:

- `Row.CostKind` (`cost_kind`), `PriceSnapshot.Source` (`source`), `PriceSnapshot.AsOf` (`as_of`) and `PriceSnapshot.UnknownRates` (`unknown_rates`) are all omitted when empty. A row written before them decodes unchanged, as `CostKindUnspecified` with every rate known, and re-encodes byte for byte.
- `costcalc.Price` changes only for a snapshot that marks a rate unknown, or a usage with `Dims`: those tokens are now counted in `Cost.UnpricedTokens` (they were already priced at $0).
- A store with columns per field adds three nullable columns (`cost_kind`, `price_source`, `price_as_of`) and five nullable booleans or one JSON column for the unknown-rate flags. Existing rows need no backfill; leave the kind unspecified rather than guess one.
- Keep token units provider-native. `usageledger.Usage` already holds the provider's disjoint counts; do not convert them before pricing.

Adopting the seam from an existing estimator:

1. **App-local per-model rate table.** Move the table into a `pricesource.Table` (or a JSON file read with `LoadFile`), with `nil` for rates you do not have, and put it in front of `FromModelsDev` with `Chain`. Take the snapshot with `src.Snapshot(provider, model)`, store it on `Row.Price`, and price the row with `costcalc.PriceRow`, so a later re-price reads the rates that were in effect (`costcalc.PriceFromSource` does the lookup and the pricing in one step when nothing is stored).
2. **Input-and-output-only pricing.** Record all five components in a `usageledger.Usage` and price through `costcalc`; an unknown cache rate now shows as partial instead of silently free.
3. **Cost backfill or re-pricing of old rows.** Price a row at its stored snapshot with `costcalc.PriceRow`. A backfill that fills a missing snapshot from a source should write `CostKind: api_estimated`, never `api_billed`.
4. **Provider invoice or billing-API figures.** Mark the row `api_billed`; that is the only kind `IsBill` accepts.
5. **Subscription cost monitors.** Price the usage through the same seam and mark the row `subscription_equivalent`, so dashboards can show it next to bills without adding it to them.
6. **Local models.** Use a table entry with the rates you assign (or `Rate(0)`) and mark the row `local_compute`.

## Compatibility

This package is pre-1.0 and unreleased: any release, including a minor one, may break the exported API, and there is no deprecation period. Pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Known limitations

- models.dev decodes an omitted optional rate (cache, reasoning) as 0, which cannot be told apart from free. `Catalog` snapshots therefore never mark a rate unknown; put an override table in front for a model whose missing rate matters.
- `Catalog` cannot tell "model not in the catalog" from "the catalog's cache was never populated"; check `modelsdev.Client.LastFetchedAt()`.
- `ParseLiteLLM` reads only the five per-token rate fields. Tiered, per-character, per-image, per-second and batch prices are ignored, and an entry with none of the five rates is skipped.
- The cache is a single JSON file per table with no locking between processes; the last `SaveFile` wins.
- Everything is USD.

## Out of scope

- Network I/O: the caller fetches a table (or runs a `modelsdev.Client` refresher) and hands it here.
- Multiplying rates against usage: that is `costcalc`.
- Currency conversion, rounding, budgets.
- Adoption in any application.
