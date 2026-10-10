# pricesource

Price sources for `costcalc.PriceSource`: a models.dev adapter, override tables, a LiteLLM-style parser, a chain and an offline file cache.

It is not a catalog client, a fetcher or a cost calculator: it turns price data the caller already has into `usageledger.PriceSnapshot` values. Do not add network I/O or rate-times-usage math here.

## Start Here

- `pricesource.go` — `Catalog`/`FromModelsDev`, `Entry`, `Table`, `FromCatalog`, `Chain`, `Live`.
- `tablefile.go` — the JSON table form, `LoadFile`/`SaveFile`, `FileCache`, `ParseLiteLLM`.
- `pricesource_test.go` — behavior tests.
- `examples/hello/main.go` — the runnable example; the README `## Usage` fence must stay identical to it.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

## Boundaries

- Dependencies: `costcalc`, `usageledger`, `modelsdev` and the standard library only. `costcalc` defines the `PriceSource` interface and must not import this package.
- No network I/O. Tests must not reach the network: build a `modelsdev.Client` over a pre-written disk cache.
- Unknown is not free. A table `Entry` rate is `*float64`; `nil` sets the matching `UnknownRates` flag and `Rate(0)` is a known free rate. Never write 0 for a rate you do not have.
- A found model is `ok == true` even when every rate is zero or unknown; "not found" comes only from the source's own lookup.
- Every snapshot sets `Source` (the table or catalog name) and `AsOf` when the source knows it, and passes `PriceSnapshot.Validate`.
- `Table` is immutable after `NewTable`; `Entries` returns a copy. Swap tables with `Live`, never mutate one in place.
- `WriteJSON` output is deterministic (sorted entries) so a cached file diffs cleanly; `ParseTable` rejects unknown fields.
- Rates are USD per million tokens in provider-native token units, matching `usageledger.PriceSnapshot`.
- Out of scope: fetching, currency, rounding, budgets, adoption in an application.
