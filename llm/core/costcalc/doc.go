// Package costcalc prices LLM token usage.
//
// It multiplies the five disjoint components of a go-usage-ledger Usage
// (uncached input, cache read, cache write, output, reasoning) by the five
// rates of a go-modelsdev Pricing, and returns a Cost whose total is derived,
// never stored. Every component is priced at its own rate; folding cache tokens
// into the input count before pricing, or reading only the input and output
// rates, understates cost.
//
// The entry points are Price (usage and a price snapshot), PriceModel (usage and
// a Catalog lookup), PriceRow (a recorded Row at its own stored snapshot),
// SnapshotPrice (the snapshot to store on a new Row) and PriceSnapshotFromPricing.
//
// A model missing from the catalog is a Cost with Priced false, not an error.
// Usage.Dims entries are not priced. There is no persistence, currency
// conversion or rounding: costs are unrounded float64 USD.
package costcalc
