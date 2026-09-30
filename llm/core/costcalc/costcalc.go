package costcalc

import (
	"github.com/hollis-labs/go-modelsdev/modelsdev"
	usageledger "github.com/hollis-labs/go-usage-ledger"
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
}

// Total sums the five per-component dollar amounts. It is derived on every
// call and never stored.
func (c Cost) Total() float64 {
	return c.UncachedInputUSD + c.CacheReadUSD + c.CacheWriteUSD + c.OutputUSD + c.ReasoningUSD
}

// Price multiplies each of usage's five core components by its own rate in
// snap. Usage.Dims entries are never priced: PriceSnapshot has no rate for an
// arbitrary extension dimension. Price never errors and does not call
// Usage.Validate; a zero rate contributes $0, whether it means "free" or "the
// catalog never populated this optional rate" (the two are indistinguishable
// in the catalog's pricing type).
func Price(usage usageledger.Usage, snap usageledger.PriceSnapshot) Cost {
	return Cost{
		UncachedInputUSD: perMillion(usage.UncachedInputTokens.Tokens, snap.InputPerMillion),
		CacheReadUSD:     perMillion(usage.CacheReadTokens.Tokens, snap.CacheReadPerMillion),
		CacheWriteUSD:    perMillion(usage.CacheWriteTokens.Tokens, snap.CacheWritePerMillion),
		OutputUSD:        perMillion(usage.OutputTokens.Tokens, snap.OutputPerMillion),
		ReasoningUSD:     perMillion(usage.ReasoningTokens.Tokens, snap.ReasoningPerMillion),
		Priced:           true,
		Provenance:       usage.TotalProvenance(),
	}
}

func perMillion(tokens int64, ratePerMillion float64) float64 {
	return float64(tokens) / 1_000_000 * ratePerMillion
}

// PriceSnapshotFromPricing converts a catalog modelsdev.Pricing into the
// usageledger.PriceSnapshot carried by a Row. The field-for-field parity of the
// two types is pinned by TestPriceSnapshotFromPricing_FieldParity.
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

// PriceRow prices a recorded Row at its own stored Price snapshot, never a
// fresh catalog lookup, so a later catalog change cannot retroactively change
// a historical cost. Do not add a variant that re-prices a Row from a live
// Catalog. A Row with a nil Price yields Cost{Priced: false}.
func PriceRow(row usageledger.Row) Cost {
	if row.Price == nil {
		return Cost{Priced: false, Provenance: row.Usage.TotalProvenance()}
	}
	return Price(row.Usage, *row.Price)
}
