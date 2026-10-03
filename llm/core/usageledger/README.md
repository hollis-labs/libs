# go-usage-ledger

Disjoint LLM token-usage ledger: fixed core plus Dims, per-component provenance, derived totals.

A `Usage` has five disjoint core components (uncached input, cache read, cache write, output, reasoning) and an open `Dims` map for provider-specific extras. Each component is a token count plus a provenance: `measured`, `estimated` or `unknown`. A component the provider did not report is `unknown`, never a silent zero. The total is not stored: `TotalTokens()` sums the components and `TotalProvenance()` reports the worst provenance among them. A `Row` wraps a `Usage` with identity and an optional `PriceSnapshot` (the rates in effect when it was recorded).

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/substrate/llm-core/usageledger
```

Requires Go 1.26.6 or newer. Standard library only.

## Usage

```go
package main

import (
	"fmt"

	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func main() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}
	// ReasoningTokens is left untouched: the provider did not report it.

	fmt.Println(u.Validate() == nil)
	fmt.Println(u.TotalTokens(), u.TotalProvenance())

	row := usageledger.Row{Provider: "anthropic", Model: "example-model", Usage: u}
	fmt.Println(row.Provider, row.Model, row.Usage.TotalTokens(), row.Price == nil)
}
```

The same program lives in [`examples/hello`](./examples/hello/main.go). Start from `NewUsage()`: it marks every core component `unknown`, so a component you never set cannot read as "measured 0". A `Usage` built by bare struct literal fails `Validate()`.

## Why

The same defect turned up in four Hollis Labs apps: Nanite stores a `total_tokens` of input plus output while cache tokens sit in their own columns; Nanite, Torque and Tesseract price only input and output (Torque folds cache tokens into the prompt count as a workaround); Tether's Anthropic adapter leaves reasoning tokens at Go's zero value, indistinguishable from "used none". This library keeps the components disjoint, derives the total, and makes "not reported" a first-class state. `TestUsage_TotalTokens_SumsAllFiveAndDims` and `TestUsage_UnreportedIsNotMeasuredZero` are the regression tests: the naive shapes fail them, this one passes.

## Provenance of this code

The design follows decision D-27 of the agent-fabric vNext register. Nothing was lifted from the apps; they were read as evidence of the defect. The claims above about Nanite, Torque, Tesseract and Tether come from reading their source at the paths the brief cited (`apps/nanite/internal/store/usage.go`, `execution_metrics.go`, `apps/torque/plugins/executor-api/anthropic.go`, `apps/tesseract/internal/contextapi/synthesis_handler.go`, `apps/tether/internal/llm/...`); none of that code was run. The regression tests reproduce the naive shapes locally rather than importing them. No behavioral equivalence with any application is claimed. Nothing uses this module yet.

## Compatibility

This module is pre-1.0 and unreleased: any release, including a minor one, may break the exported API, and there is no deprecation period. Pin an exact version and read [CHANGELOG.md](./CHANGELOG.md) before upgrading.

## Known limitations

- `TotalTokens()` is a floor, not a fact, whenever `TotalProvenance()` is not `measured`: an unknown component contributes 0.
- Sums are plain `int64` addition and do not check for overflow.
- `Validate()` is not called for you: `Row` has no constructor and nothing validates on unmarshal. Call `Usage.Validate()` before persisting or trusting a decoded value. A decoded `Usage` with an absent component has empty provenance and fails validation.
- `PriceSnapshot` copies the shape of a model catalog's pricing type by value. If the catalog adds a rate, this type does not follow until someone changes it; a parity test belongs in the library that imports both.
- `Row` and `PriceSnapshot` are not validated at all (no check for negative rates or empty identity).
- `Dims` names are free-form apart from the collision and empty-name rules; nothing normalizes their case, so `"Audio"` and `"audio"` are two dimensions.

## Out of scope

- Cost and pricing calculation: multiplying a `Usage` by rates, currency, budgets, price backfill. That belongs in a separate library (`go-modelsdev-catalog-helpers`); this one has no cost method or field.
- Persistence and storage: no SQL schema, store interface or migrations. The package defines the in-memory and wire shape only.
- Converting a provider's response type (or `go-llm-types.Usage`) into a `Usage` or `Row`; each adopting application does that.
- Adoption by any application. No application uses this module yet.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
