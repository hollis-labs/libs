package main

import (
	"fmt"

	costcalc "github.com/hollis-labs/go-modelsdev-catalog-helpers"
	"github.com/hollis-labs/go-modelsdev/modelsdev"
	usageledger "github.com/hollis-labs/go-usage-ledger"
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
