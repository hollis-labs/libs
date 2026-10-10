package costcalc_test

import (
	"testing"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

// fakeSource is a test double for costcalc.PriceSource.
type fakeSource map[string]usageledger.PriceSnapshot

func (f fakeSource) Snapshot(providerID, modelID string) (usageledger.PriceSnapshot, bool) {
	s, ok := f[providerID+"/"+modelID]
	return s, ok
}

var _ costcalc.PriceSource = fakeSource(nil)

func TestPrice_KnownRatesAreNotPartial(t *testing.T) {
	c := costcalc.Price(fullUsage(), fullSnap)
	if c.Partial() || c.UnpricedTokens != 0 {
		t.Fatalf("fully priced usage reported partial: %+v", c)
	}
}

// An unknown cache-read rate must not read as free: those tokens contribute
// $0, are counted as unpriced, and the cost is partial.
func TestPrice_UnknownRateIsPartialNotFree(t *testing.T) {
	snap := fullSnap
	snap.CacheReadPerMillion = 0
	snap.UnknownRates.CacheRead = true
	c := costcalc.Price(fullUsage(), snap)
	if c.CacheReadUSD != 0 {
		t.Fatalf("CacheReadUSD = %v, want 0", c.CacheReadUSD)
	}
	if !near(c.Total(), fullWant-0.6) {
		t.Fatalf("Total() = %v, want %v", c.Total(), fullWant-0.6)
	}
	if c.UnpricedTokens != 2_000_000 {
		t.Fatalf("UnpricedTokens = %d, want 2000000 (the cache-read tokens)", c.UnpricedTokens)
	}
	if !c.Partial() || !c.Priced {
		t.Fatalf("want Priced and Partial, got %+v", c)
	}
}

// A zero rate that is not marked unknown is free: priced, not partial.
func TestPrice_ZeroKnownRateIsFreeNotPartial(t *testing.T) {
	snap := fullSnap
	snap.CacheWritePerMillion = 0
	c := costcalc.Price(fullUsage(), snap)
	if c.Partial() {
		t.Fatalf("a known zero rate made the cost partial: %+v", c)
	}
}

// An unknown rate on a component with no tokens leaves nothing unpriced.
func TestPrice_UnknownRateWithNoTokensIsNotPartial(t *testing.T) {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = comp(1000, usageledger.ProvenanceMeasured)
	u.OutputTokens = comp(10, usageledger.ProvenanceMeasured)
	snap := fullSnap
	snap.ReasoningPerMillion = 0
	snap.UnknownRates.Reasoning = true
	if c := costcalc.Price(u, snap); c.Partial() {
		t.Fatalf("got partial with zero reasoning tokens: %+v", c)
	}
}

func TestPrice_EveryUnknownRateIsCounted(t *testing.T) {
	snap := usageledger.PriceSnapshot{UnknownRates: usageledger.UnknownRates{
		UncachedInput: true, CacheRead: true, CacheWrite: true, Output: true, Reasoning: true,
	}}
	u := fullUsage()
	c := costcalc.Price(u, snap)
	if c.Total() != 0 || c.UnpricedTokens != u.TotalTokens() || !c.Partial() {
		t.Fatalf("all-unknown snapshot: %+v (total tokens %d)", c, u.TotalTokens())
	}
}

// Cache tokens are priced at their own rates, never folded into input, and
// an unknown cache-write rate leaves only the cache-write tokens unpriced.
func TestPrice_CacheComponentsWithPartialRates(t *testing.T) {
	m := usageledger.ProvenanceMeasured
	u := usageledger.NewUsage()
	u.UncachedInputTokens = comp(100_000, m)
	u.CacheReadTokens = comp(900_000, m)
	u.CacheWriteTokens = comp(50_000, m)
	u.OutputTokens = comp(20_000, m)
	snap := usageledger.PriceSnapshot{
		InputPerMillion:     3,
		OutputPerMillion:    15,
		CacheReadPerMillion: 0.3,
		UnknownRates:        usageledger.UnknownRates{CacheWrite: true},
	}
	c := costcalc.Price(u, snap)
	// 0.1*3 + 0.9*0.3 + 0.02*15; cache write unknown.
	if want := 0.3 + 0.27 + 0.3; !near(c.Total(), want) {
		t.Fatalf("Total() = %v, want %v", c.Total(), want)
	}
	if !near(c.CacheReadUSD, 0.27) {
		t.Fatalf("CacheReadUSD = %v, want 0.27 (cache read at its own rate)", c.CacheReadUSD)
	}
	if c.UnpricedTokens != 50_000 {
		t.Fatalf("UnpricedTokens = %d, want 50000", c.UnpricedTokens)
	}
}

func TestPrice_DimsCountAsUnpriced(t *testing.T) {
	u := fullUsage()
	u.SetDim("audio_input", comp(7, usageledger.ProvenanceMeasured))
	c := costcalc.Price(u, fullSnap)
	if c.UnpricedTokens != 7 || !c.Partial() {
		t.Fatalf("dims tokens not counted as unpriced: %+v", c)
	}
	if !near(c.Total(), fullWant) {
		t.Fatalf("Dims changed the price: %v", c.Total())
	}
}

func TestPriceRow_CarriesCostKind(t *testing.T) {
	snap := fullSnap
	for _, k := range []usageledger.CostKind{
		usageledger.CostKindUnspecified, usageledger.CostKindAPIBilled, usageledger.CostKindAPIEstimated,
		usageledger.CostKindSubscriptionEquivalent, usageledger.CostKindLocalCompute,
	} {
		c := costcalc.PriceRow(usageledger.Row{Usage: fullUsage(), Price: &snap, CostKind: k})
		if c.Kind != k {
			t.Errorf("%q: Kind = %q", k, c.Kind)
		}
		if got, want := c.IsBill(), k == usageledger.CostKindAPIBilled; got != want {
			t.Errorf("%q: IsBill() = %v, want %v", k, got, want)
		}
	}
}

// A subscription-equivalent figure is a real number but never a bill.
func TestPriceRow_SubscriptionEquivalentIsNotABill(t *testing.T) {
	snap := fullSnap
	c := costcalc.PriceRow(usageledger.Row{Usage: fullUsage(), Price: &snap, CostKind: usageledger.CostKindSubscriptionEquivalent})
	if !near(c.Total(), fullWant) {
		t.Fatalf("Total() = %v, want %v", c.Total(), fullWant)
	}
	if c.IsBill() {
		t.Fatal("subscription-equivalent cost reported as a bill")
	}
}

func TestPriceRow_UnpricedBilledRowIsNotABill(t *testing.T) {
	c := costcalc.PriceRow(usageledger.Row{Usage: fullUsage(), CostKind: usageledger.CostKindAPIBilled})
	if c.Priced || c.IsBill() || c.Kind != usageledger.CostKindAPIBilled {
		t.Fatalf("got %+v", c)
	}
}

func TestPriceFromSource(t *testing.T) {
	src := fakeSource{"anthropic/m": fullSnap}
	c := costcalc.PriceFromSource(src, "anthropic", "m", fullUsage())
	if !c.Priced || !near(c.Total(), fullWant) || c.Kind != usageledger.CostKindUnspecified {
		t.Fatalf("found: %+v", c)
	}
	u := fullUsage()
	c = costcalc.PriceFromSource(src, "anthropic", "missing", u)
	if c.Priced || c.Total() != 0 || c.Provenance != u.TotalProvenance() {
		t.Fatalf("missing: %+v", c)
	}
}

// A zero-price entry in a source is priced (free), not missing.
func TestPriceFromSource_ZeroPriceIsPriced(t *testing.T) {
	src := fakeSource{"local/m": {}}
	c := costcalc.PriceFromSource(src, "local", "m", fullUsage())
	if !c.Priced || c.Total() != 0 || c.Partial() {
		t.Fatalf("got %+v", c)
	}
}
