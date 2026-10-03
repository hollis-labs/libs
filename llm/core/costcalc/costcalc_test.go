package costcalc_test

import (
	"math"
	"reflect"
	"testing"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

// fakeCatalog is a test double for costcalc.Catalog.
type fakeCatalog map[string]modelsdev.Model

func (f fakeCatalog) Get(providerID, modelID string) (modelsdev.Model, bool) {
	m, ok := f[providerID+"/"+modelID]
	return m, ok
}

func comp(tokens int64, p usageledger.Provenance) usageledger.Component {
	return usageledger.Component{Tokens: tokens, Provenance: p}
}

// fullUsage has all five core components measured and non-zero.
func fullUsage() usageledger.Usage {
	m := usageledger.ProvenanceMeasured
	u := usageledger.NewUsage()
	u.UncachedInputTokens = comp(1_000_000, m)
	u.CacheReadTokens = comp(2_000_000, m)
	u.CacheWriteTokens = comp(500_000, m)
	u.OutputTokens = comp(250_000, m)
	u.ReasoningTokens = comp(100_000, m)
	return u
}

// fullSnap has five distinct rates so a swapped field changes the total.
var fullSnap = usageledger.PriceSnapshot{
	InputPerMillion:      3,
	OutputPerMillion:     15,
	CacheWritePerMillion: 3.75,
	CacheReadPerMillion:  0.3,
	ReasoningPerMillion:  12,
}

// Hand-computed: 1*3 + 2*0.3 + 0.5*3.75 + 0.25*15 + 0.1*12.
const fullWant = 3 + 0.6 + 1.875 + 3.75 + 1.2

// naiveInputOutputOnly is the shape found in Nanite (estimateCost), Tesseract
// (computeCost) and Torque (modelcatalog.EstimateCost): only input and output
// are priced. Reproduced locally; the app code was read, not run.
func naiveInputOutputOnly(u usageledger.Usage, s usageledger.PriceSnapshot) float64 {
	return float64(u.UncachedInputTokens.Tokens)*s.InputPerMillion/1e6 +
		float64(u.OutputTokens.Tokens)*s.OutputPerMillion/1e6
}

// naiveFoldThenPrice is Torque's pipeline: cache tokens are folded into the
// prompt count, then the whole prompt is priced at the input rate.
func naiveFoldThenPrice(u usageledger.Usage, s usageledger.PriceSnapshot) float64 {
	prompt := u.UncachedInputTokens.Tokens + u.CacheReadTokens.Tokens + u.CacheWriteTokens.Tokens
	return float64(prompt)*s.InputPerMillion/1e6 + float64(u.OutputTokens.Tokens)*s.OutputPerMillion/1e6
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPrice_AllFiveComponentsPriced(t *testing.T) {
	u := fullUsage()
	c := costcalc.Price(u, fullSnap)
	if !near(c.Total(), fullWant) {
		t.Fatalf("Total() = %v, want %v", c.Total(), fullWant)
	}
	if !near(c.UncachedInputUSD, 3) || !near(c.CacheReadUSD, 0.6) || !near(c.CacheWriteUSD, 1.875) ||
		!near(c.OutputUSD, 3.75) || !near(c.ReasoningUSD, 1.2) {
		t.Fatalf("per-component amounts wrong: %+v", c)
	}
	if !c.Priced {
		t.Fatal("Priced = false")
	}

	// The regression: each naive shape prices this usage wrongly.
	if got := naiveInputOutputOnly(u, fullSnap); near(got, fullWant) {
		t.Fatalf("input/output-only naive shape unexpectedly matched: %v", got)
	}
	if got := naiveFoldThenPrice(u, fullSnap); near(got, fullWant) {
		t.Fatalf("fold-then-price naive shape unexpectedly matched: %v", got)
	}
}

// TestPrice_NaiveShapesUnderstate pins how far off each naive shape is on the
// same input, so the regression is a number and not just "different".
func TestPrice_NaiveShapesUnderstate(t *testing.T) {
	u := fullUsage()
	if got, want := naiveInputOutputOnly(u, fullSnap), 3+3.75; !near(got, want) {
		t.Fatalf("input/output-only = %v, want %v", got, want)
	}
	// The fold prices 3.5M prompt tokens at the input rate: 10.5 + 3.75.
	if got, want := naiveFoldThenPrice(u, fullSnap), 10.5+3.75; !near(got, want) {
		t.Fatalf("fold-then-price = %v, want %v", got, want)
	}
	// The fold overstates here (cache read is cheaper than input) and
	// understates elsewhere; the point is that neither equals the true cost.
	c := costcalc.Price(u, fullSnap)
	if c.Total() >= naiveFoldThenPrice(u, fullSnap) {
		t.Fatalf("expected fold to overstate on cache-heavy usage; lib=%v fold=%v", c.Total(), naiveFoldThenPrice(u, fullSnap))
	}
	if c.Total() <= naiveInputOutputOnly(u, fullSnap) {
		t.Fatalf("expected input/output-only to understate; lib=%v naive=%v", c.Total(), naiveInputOutputOnly(u, fullSnap))
	}
}

func TestPrice_EachComponentUsesItsOwnRate(t *testing.T) {
	m := usageledger.ProvenanceMeasured
	set := map[string]func(*usageledger.Usage){
		"input":     func(u *usageledger.Usage) { u.UncachedInputTokens = comp(1_000_000, m) },
		"cacheRead": func(u *usageledger.Usage) { u.CacheReadTokens = comp(1_000_000, m) },
		"cacheWrit": func(u *usageledger.Usage) { u.CacheWriteTokens = comp(1_000_000, m) },
		"output":    func(u *usageledger.Usage) { u.OutputTokens = comp(1_000_000, m) },
		"reasoning": func(u *usageledger.Usage) { u.ReasoningTokens = comp(1_000_000, m) },
	}
	want := map[string]float64{"input": 3, "cacheRead": 0.3, "cacheWrit": 3.75, "output": 15, "reasoning": 12}
	for name, f := range set {
		u := usageledger.NewUsage()
		f(&u)
		if got := costcalc.Price(u, fullSnap).Total(); !near(got, want[name]) {
			t.Errorf("%s: Total() = %v, want %v", name, got, want[name])
		}
	}
}

func TestPrice_ZeroRateContributesZero(t *testing.T) {
	snap := fullSnap
	snap.CacheWritePerMillion = 0
	c := costcalc.Price(fullUsage(), snap)
	if c.CacheWriteUSD != 0 {
		t.Fatalf("CacheWriteUSD = %v, want 0", c.CacheWriteUSD)
	}
	if !near(c.Total(), fullWant-1.875) {
		t.Fatalf("Total() = %v, want %v", c.Total(), fullWant-1.875)
	}
	if !c.Priced {
		t.Fatal("zero rate must not make the cost unpriced")
	}
}

func TestPrice_DimsNotPriced(t *testing.T) {
	u := fullUsage()
	u.SetDim("tool_input", comp(9_000_000, usageledger.ProvenanceMeasured))
	if got := costcalc.Price(u, fullSnap).Total(); !near(got, fullWant) {
		t.Fatalf("Dims changed the price: %v, want %v", got, fullWant)
	}
}

func TestPrice_TotalIsDerived(t *testing.T) {
	// Cost has exactly five USD fields plus Priced and Provenance: no stored total.
	typ := reflect.TypeOf(costcalc.Cost{})
	want := []string{"UncachedInputUSD", "CacheReadUSD", "CacheWriteUSD", "OutputUSD", "ReasoningUSD", "Priced", "Provenance"}
	if typ.NumField() != len(want) {
		t.Fatalf("Cost has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, n := range want {
		if typ.Field(i).Name != n {
			t.Errorf("field %d = %s, want %s", i, typ.Field(i).Name, n)
		}
	}
	c := costcalc.Cost{OutputUSD: 1}
	c.CacheReadUSD = 2
	if c.Total() != 3 {
		t.Fatalf("Total() did not follow a component edit: %v", c.Total())
	}
}

func TestPrice_ProvenancePropagates(t *testing.T) {
	m := usageledger.ProvenanceMeasured
	u := usageledger.NewUsage() // reasoning stays unknown, as with Anthropic
	u.UncachedInputTokens = comp(10, m)
	u.CacheReadTokens = comp(10, m)
	u.CacheWriteTokens = comp(10, m)
	u.OutputTokens = comp(10, m)
	if got := costcalc.Price(u, fullSnap).Provenance; got != usageledger.ProvenanceUnknown {
		t.Fatalf("Provenance = %q, want unknown", got)
	}
	if got := costcalc.Price(fullUsage(), fullSnap).Provenance; got != usageledger.ProvenanceMeasured {
		t.Fatalf("Provenance = %q, want measured", got)
	}
	u.ReasoningTokens = comp(1, usageledger.ProvenanceEstimated)
	if got := costcalc.Price(u, fullSnap).Provenance; got != usageledger.ProvenanceEstimated {
		t.Fatalf("Provenance = %q, want estimated", got)
	}
}

func TestPrice_DimsUncertaintyStillReachesProvenance(t *testing.T) {
	u := fullUsage()
	u.SetDim("audio", comp(0, usageledger.ProvenanceUnknown))
	c := costcalc.Price(u, fullSnap)
	if c.Provenance != usageledger.ProvenanceUnknown || c.Provenance != u.TotalProvenance() {
		t.Fatalf("Provenance = %q, want %q (mirrors Usage.TotalProvenance)", c.Provenance, u.TotalProvenance())
	}
	if !near(c.Total(), fullWant) {
		t.Fatalf("Total() = %v, want %v", c.Total(), fullWant)
	}
}

func TestPriceModel_ModelNotFound(t *testing.T) {
	u := usageledger.NewUsage() // all unknown
	c := costcalc.PriceModel(fakeCatalog{}, "anthropic", "nope", u)
	if c.Priced {
		t.Fatal("Priced = true for an uncataloged model")
	}
	if c.Total() != 0 || c.UncachedInputUSD != 0 || c.CacheReadUSD != 0 || c.CacheWriteUSD != 0 || c.OutputUSD != 0 || c.ReasoningUSD != 0 {
		t.Fatalf("USD fields not zero: %+v", c)
	}
	if c.Provenance != u.TotalProvenance() || c.Provenance != usageledger.ProvenanceUnknown {
		t.Fatalf("Provenance = %q, want %q", c.Provenance, u.TotalProvenance())
	}
	mc := costcalc.PriceModel(fakeCatalog{}, "anthropic", "nope", fullUsage())
	if mc.Provenance != usageledger.ProvenanceMeasured {
		t.Fatalf("Provenance = %q, want measured (independent of price availability)", mc.Provenance)
	}
}

func TestPriceModel_ZeroPriceIsPricedNotUnpriced(t *testing.T) {
	// A found model whose Cost is all zero (open weights). Torque's
	// modelcatalog.Pricing reports this as not-found; this lib must not.
	cat := fakeCatalog{"local/free": {ID: "free"}}
	c := costcalc.PriceModel(cat, "local", "free", fullUsage())
	if !c.Priced {
		t.Fatal("Priced = false for a found, zero-priced model")
	}
	if c.Total() != 0 {
		t.Fatalf("Total() = %v, want 0", c.Total())
	}
	missing := costcalc.PriceModel(cat, "local", "absent", fullUsage())
	if missing.Priced == c.Priced {
		t.Fatal("found-free and not-found must be distinct outcomes")
	}
}

func TestPriceModel_Found(t *testing.T) {
	cat := fakeCatalog{"anthropic/m": {ID: "m", Cost: modelsdev.Pricing{
		Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3, Reasoning: 12,
	}}}
	c := costcalc.PriceModel(cat, "anthropic", "m", fullUsage())
	if !c.Priced || !near(c.Total(), fullWant) {
		t.Fatalf("got %+v, want priced total %v", c, fullWant)
	}
}

func TestPriceSnapshotFromPricing_FieldParity(t *testing.T) {
	pt := reflect.TypeOf(modelsdev.Pricing{})
	st := reflect.TypeOf(usageledger.PriceSnapshot{})
	if pt.NumField() != st.NumField() {
		t.Fatalf("modelsdev.Pricing has %d fields, usageledger.PriceSnapshot has %d: a pricing dimension was added on one side", pt.NumField(), st.NumField())
	}
	// Give every Pricing field a distinct non-zero value, convert, and require
	// each one to land in the PriceSnapshot field named <Name>PerMillion.
	var p modelsdev.Pricing
	pv := reflect.ValueOf(&p).Elem()
	for i := range pt.NumField() {
		if pt.Field(i).Type.Kind() != reflect.Float64 {
			t.Fatalf("Pricing.%s is %s, not float64", pt.Field(i).Name, pt.Field(i).Type)
		}
		pv.Field(i).SetFloat(float64(i + 1))
	}
	sv := reflect.ValueOf(costcalc.PriceSnapshotFromPricing(p))
	for i := range pt.NumField() {
		name := pt.Field(i).Name + "PerMillion"
		f := sv.FieldByName(name)
		if !f.IsValid() {
			t.Fatalf("PriceSnapshot has no field %s for Pricing.%s", name, pt.Field(i).Name)
		}
		if f.Float() != float64(i+1) {
			t.Errorf("Pricing.%s -> %s = %v, want %v", pt.Field(i).Name, name, f.Float(), float64(i+1))
		}
	}
}

func TestPriceRow_NilPriceIsUnpriced(t *testing.T) {
	u := usageledger.NewUsage()
	c := costcalc.PriceRow(usageledger.Row{Usage: u})
	if c.Priced || c.Total() != 0 || c.Provenance != u.TotalProvenance() {
		t.Fatalf("got %+v", c)
	}
}

func TestPriceRow_UsesStoredSnapshotNotLiveCatalog(t *testing.T) {
	stored := fullSnap
	cat := fakeCatalog{"anthropic/m": {ID: "m", Cost: modelsdev.Pricing{Input: 300, Output: 1500}}}

	// Record the row at today's rates, then the catalog re-prices.
	row := usageledger.Row{Provider: "anthropic", Model: "m", Usage: fullUsage(), Price: &stored}
	live := costcalc.PriceModel(cat, row.Provider, row.Model, row.Usage)
	if near(live.Total(), fullWant) {
		t.Fatal("test setup: live catalog should price differently")
	}
	if got := costcalc.PriceRow(row).Total(); !near(got, fullWant) {
		t.Fatalf("PriceRow = %v, want stored-snapshot cost %v", got, fullWant)
	}
}

func TestSnapshotPrice_UnknownModel(t *testing.T) {
	snap, ok := costcalc.SnapshotPrice(fakeCatalog{}, "p", "m")
	if ok || snap != (usageledger.PriceSnapshot{}) {
		t.Fatalf("got %+v, %v", snap, ok)
	}
}

func TestSnapshotPrice_Found(t *testing.T) {
	cat := fakeCatalog{"p/m": {ID: "m", Cost: modelsdev.Pricing{Input: 1, Output: 2, CacheWrite: 3, CacheRead: 4, Reasoning: 5}}}
	snap, ok := costcalc.SnapshotPrice(cat, "p", "m")
	want := usageledger.PriceSnapshot{InputPerMillion: 1, OutputPerMillion: 2, CacheWritePerMillion: 3, CacheReadPerMillion: 4, ReasoningPerMillion: 5}
	if !ok || snap != want {
		t.Fatalf("got %+v, %v; want %+v", snap, ok, want)
	}
	free, ok := costcalc.SnapshotPrice(fakeCatalog{"p/f": {ID: "f"}}, "p", "f")
	if !ok || free != (usageledger.PriceSnapshot{}) {
		t.Fatalf("free model: got %+v, %v", free, ok)
	}
}
