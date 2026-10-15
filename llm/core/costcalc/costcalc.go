package costcalc

import (
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

// Catalog is the minimal read surface this package needs from a pricing
// source. *modelsdev.Client satisfies it. PriceModel and SnapshotPrice accept
// this interface rather than the concrete type so tests can supply a fake and
// so an application that wraps its own catalog need not expose the raw client.
type Catalog interface {
	Get(providerID, modelID string) (modelsdev.Model, bool)
}

var _ Catalog = (*modelsdev.Client)(nil)

// Cost is the priced result of one usageledger.Usage against one
// usageledger.PriceSnapshot. Like Usage.TotalTokens, the total is not a stored
// field: Total sums the five per-component dollar amounts on every call, so it
// cannot drift from them.
type Cost struct {
	UncachedInputUSD float64
	CacheReadUSD     float64
	CacheWriteUSD    float64
	OutputUSD        float64
	ReasoningUSD     float64

	// Priced is false only when no price snapshot could be resolved at all
	// (the model is absent from the catalog, or a Row carries no Price). All
	// five USD fields are 0 in that case. Check Priced before reading a 0
	// total as "this call was free".
	Priced bool

	// Provenance is usage.TotalProvenance(), carried through unchanged. Treat
	// Total as a lower bound whenever this is not usageledger.ProvenanceMeasured:
	// an unknown component has 0 tokens and so contributes $0 by construction.
	// It also reflects Dims entries, which are never priced here.
	Provenance usageledger.Provenance

	// Kind is the row's cost kind (PriceRow) or CostKindUnspecified (Price,
	// PriceModel, PriceFromSource; set it yourself). Only an api_billed cost
	// is a bill: see IsBill.
	Kind usageledger.CostKind

	// UnpricedTokens counts the tokens that had no known rate: core
	// components whose rate the snapshot marks unknown, plus every Dims
	// entry. They contribute $0, so a Cost with UnpricedTokens > 0 is partial
	// (see Partial) and its Total is a lower bound.
	UnpricedTokens int64
}

// Total sums the five per-component dollar amounts. It is derived on every
// call and never stored.
func (c Cost) Total() float64 {
	return c.UncachedInputUSD + c.CacheReadUSD + c.CacheWriteUSD + c.OutputUSD + c.ReasoningUSD
}

// Partial reports whether some tokens were left unpriced because their rate
// was unknown (or they sit in Dims). Total is then a lower bound. An unpriced
// Cost (Priced false) is not partial; it has no price at all.
func (c Cost) Partial() bool { return c.Priced && c.UnpricedTokens > 0 }

// IsBill reports whether the cost is money a provider charges: Kind is
// usageledger.CostKindAPIBilled and a price was resolved. Subscription
// equivalents, local compute, estimates and unspecified kinds are never bills.
func (c Cost) IsBill() bool { return c.Priced && c.Kind.IsBill() }

// Price multiplies each of usage's five core components by its own rate in
// snap. A component whose rate snap marks unknown (UnknownRates) contributes
// $0 and its tokens are counted in UnpricedTokens. Usage.Dims entries are
// never priced and always count as unpriced: PriceSnapshot has no rate for an
// arbitrary extension dimension. Price never errors and does not call
// Usage.Validate; a zero rate that is not marked unknown contributes $0,
// whether it means "free" or "the source never populated this optional rate".
func Price(usage usageledger.Usage, snap usageledger.PriceSnapshot) Cost {
	var unpriced int64
	priced := func(name string, c usageledger.Component, rate float64) float64 {
		if !snap.RateKnown(name) {
			unpriced += c.Tokens
			return 0
		}
		return perMillion(c.Tokens, rate)
	}
	cost := Cost{
		UncachedInputUSD: priced("uncached_input_tokens", usage.UncachedInputTokens, snap.InputPerMillion),
		CacheReadUSD:     priced("cache_read_tokens", usage.CacheReadTokens, snap.CacheReadPerMillion),
		CacheWriteUSD:    priced("cache_write_tokens", usage.CacheWriteTokens, snap.CacheWritePerMillion),
		OutputUSD:        priced("output_tokens", usage.OutputTokens, snap.OutputPerMillion),
		ReasoningUSD:     priced("reasoning_tokens", usage.ReasoningTokens, snap.ReasoningPerMillion),
		Priced:           true,
		Provenance:       usage.TotalProvenance(),
	}
	for _, d := range usage.Dims {
		unpriced += d.Tokens
	}
	cost.UnpricedTokens = unpriced
	return cost
}

func perMillion(tokens int64, ratePerMillion float64) float64 {
	return float64(tokens) / 1_000_000 * ratePerMillion
}

// PriceSnapshotFromPricing converts a catalog modelsdev.Pricing into the
// usageledger.PriceSnapshot carried by a Row. The field-for-field parity of the
// rates is pinned by TestPriceSnapshotFromPricing_FieldParity. It leaves
// Source, AsOf and UnknownRates empty: models.dev omits an optional rate it
// does not know, so a 0 here cannot be told apart from free.
func PriceSnapshotFromPricing(p modelsdev.Pricing) usageledger.PriceSnapshot {
	return usageledger.PriceSnapshot{
		InputPerMillion:      p.Input,
		OutputPerMillion:     p.Output,
		CacheWritePerMillion: p.CacheWrite,
		CacheReadPerMillion:  p.CacheRead,
		ReasoningPerMillion:  p.Reasoning,
	}
}

// SnapshotPrice looks up (providerID, modelID) in cat and returns the
// PriceSnapshot to store on a new usageledger.Row when it is recorded. ok is
// cat.Get's own bool: "not found" is never inferred from a zero price, so a
// free model yields an all-zero snapshot with ok=true.
func SnapshotPrice(cat Catalog, providerID, modelID string) (snap usageledger.PriceSnapshot, ok bool) {
	m, found := cat.Get(providerID, modelID)
	if !found {
		return usageledger.PriceSnapshot{}, false
	}
	return PriceSnapshotFromPricing(m.Cost), true
}

// PriceModel resolves (providerID, modelID) in cat and prices usage at the
// resulting snapshot. A model absent from the catalog (or a catalog whose
// cache was never populated; the two are indistinguishable through Get) yields
// Cost{Priced: false} with Provenance still taken from usage: a representable
// outcome, not an error.
func PriceModel(cat Catalog, providerID, modelID string, usage usageledger.Usage) Cost {
	snap, ok := SnapshotPrice(cat, providerID, modelID)
	if !ok {
		return Cost{Priced: false, Provenance: usage.TotalProvenance()}
	}
	return Price(usage, snap)
}

// PriceSource is the seam between pricing and wherever rates come from: a
// model catalog, an override table, a cached copy of either, or a chain of
// them. Snapshot returns the rates to store on a new Row, with Source, AsOf
// and UnknownRates filled in as far as the source knows them. ok is false
// when the source has no entry for the model; it is never inferred from a
// zero price. The pricesource package provides implementations.
type PriceSource interface {
	Snapshot(providerID, modelID string) (snap usageledger.PriceSnapshot, ok bool)
}

// PriceFromSource resolves (providerID, modelID) in src and prices usage at
// the resulting snapshot, like PriceModel does for a Catalog. A model the
// source does not have yields Cost{Priced: false}. Kind is left unspecified:
// the caller knows whether the call is billed, estimated, covered by a
// subscription or run locally.
func PriceFromSource(src PriceSource, providerID, modelID string, usage usageledger.Usage) Cost {
	snap, ok := src.Snapshot(providerID, modelID)
	if !ok {
		return Cost{Priced: false, Provenance: usage.TotalProvenance()}
	}
	return Price(usage, snap)
}

// PriceRow prices a recorded Row at its own stored Price snapshot, never a
// fresh catalog lookup, so a later catalog change cannot retroactively change
// a historical cost. Do not add a variant that re-prices a Row from a live
// Catalog. A Row with a nil Price yields Cost{Priced: false}. The Cost carries
// the row's CostKind.
func PriceRow(row usageledger.Row) Cost {
	if row.Price == nil {
		return Cost{Priced: false, Provenance: row.Usage.TotalProvenance(), Kind: row.CostKind}
	}
	c := Price(row.Usage, *row.Price)
	c.Kind = row.CostKind
	return c
}
