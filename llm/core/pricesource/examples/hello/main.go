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
