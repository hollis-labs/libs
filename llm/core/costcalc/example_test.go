package costcalc_test

import (
	"fmt"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	"github.com/hollis-labs/substrate/llm-core/modelsdev"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

func exampleUsage() usageledger.Usage {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1_000_000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 2_000_000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500_000, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 250_000, Provenance: usageledger.ProvenanceMeasured}
	return u // ReasoningTokens stays unknown: the provider did not report it.
}

func ExamplePrice() {
	snap := usageledger.PriceSnapshot{InputPerMillion: 3, OutputPerMillion: 15, CacheWritePerMillion: 3.75, CacheReadPerMillion: 0.3}
	c := costcalc.Price(exampleUsage(), snap)
	fmt.Printf("%.4f %s %t\n", c.Total(), c.Provenance, c.Priced)
	// Output: 9.2250 unknown true
}

type oneModel struct{}

func (oneModel) Get(providerID, modelID string) (modelsdev.Model, bool) {
	if providerID == "anthropic" && modelID == "example-model" {
		return modelsdev.Model{ID: modelID, Cost: modelsdev.Pricing{Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3}}, true
	}
	return modelsdev.Model{}, false
}

func ExamplePriceModel() {
	known := costcalc.PriceModel(oneModel{}, "anthropic", "example-model", exampleUsage())
	unknown := costcalc.PriceModel(oneModel{}, "anthropic", "other", exampleUsage())
	fmt.Printf("%.4f %t\n", known.Total(), known.Priced)
	fmt.Printf("%.4f %t\n", unknown.Total(), unknown.Priced)
	// Output:
	// 9.2250 true
	// 0.0000 false
}

func ExamplePriceSnapshotFromPricing() {
	snap := costcalc.PriceSnapshotFromPricing(modelsdev.Pricing{Input: 3, Output: 15, CacheRead: 0.3})
	fmt.Println(snap.InputPerMillion, snap.OutputPerMillion, snap.CacheReadPerMillion, snap.CacheWritePerMillion)
	// Output: 3 15 0.3 0
}

func ExampleSnapshotPrice() {
	snap, ok := costcalc.SnapshotPrice(oneModel{}, "anthropic", "example-model")
	var row usageledger.Row
	if ok {
		row.Price = &snap // store the snapshot on the row when it is recorded
	}
	fmt.Println(ok, row.Price.OutputPerMillion)
	// Output: true 15
}

func ExamplePriceRow() {
	snap := usageledger.PriceSnapshot{InputPerMillion: 3, OutputPerMillion: 15}
	row := usageledger.Row{Usage: exampleUsage(), Price: &snap}
	fmt.Printf("%.2f\n", costcalc.PriceRow(row).Total())
	row.Price = nil
	fmt.Println(costcalc.PriceRow(row).Priced)
	// Output:
	// 6.75
	// false
}

func ExampleCost_Total() {
	c := costcalc.Cost{UncachedInputUSD: 1, CacheReadUSD: 0.5, OutputUSD: 2}
	fmt.Println(c.Total())
	// Output: 3.5
}

// Example_quickstart mirrors examples/hello/main.go and the README usage fence.
func Example_quickstart() {
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 1200, Provenance: usageledger.ProvenanceMeasured}
	u.CacheReadTokens = usageledger.Component{Tokens: 8000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 500, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 300, Provenance: usageledger.ProvenanceMeasured}
	// ReasoningTokens is left untouched: the provider did not report it.

	pricing := modelsdev.Pricing{Input: 3, Output: 15, CacheWrite: 3.75, CacheRead: 0.3}
	cost := costcalc.Price(u, costcalc.PriceSnapshotFromPricing(pricing))

	fmt.Printf("$%.6f priced=%t provenance=%s\n", cost.Total(), cost.Priced, cost.Provenance)
	// Output: $0.012375 priced=true provenance=unknown
}
