package costcalc_test

import (
	"encoding/json"
	"testing"

	"github.com/hollis-labs/libs/llm/core/costcalc"
	"github.com/hollis-labs/libs/llm/core/modelsdev"
	"github.com/hollis-labs/libs/llm/core/usageledger"
)

func TestDecodedRatesProducePartialCost(t *testing.T) {
	var p modelsdev.Pricing
	if err := json.Unmarshal([]byte(`{"input":0,"output":2,"cache_read":0}`), &p); err != nil {
		t.Fatal(err)
	}
	snap := costcalc.PriceSnapshotFromPricing(p)
	if err := snap.Validate(); err != nil {
		t.Fatal(err)
	}
	u := usageledger.NewUsage()
	u.UncachedInputTokens = usageledger.Component{Tokens: 100, Provenance: usageledger.ProvenanceMeasured}
	u.OutputTokens = usageledger.Component{Tokens: 1000000, Provenance: usageledger.ProvenanceMeasured}
	u.CacheWriteTokens = usageledger.Component{Tokens: 50, Provenance: usageledger.ProvenanceMeasured}
	c := costcalc.Price(u, snap)
	if !c.Priced || !c.Partial() || c.Total() != 2 || c.UnpricedTokens != 50 {
		t.Fatalf("cost: %+v", c)
	}
	if snap.UnknownRates.UncachedInput || !snap.UnknownRates.CacheWrite {
		t.Fatalf("availability: %+v", snap)
	}
}

func TestContextFreeSnapshotDoesNotGuessTier(t *testing.T) {
	for _, raw := range []string{
		`{"input":1,"output":2,"tiers":[{"tier":{"type":"context","size":200000},"input":2,"output":4}]}`,
		`{"input":1,"output":2,"context_over_200k":{"input":2,"output":4}}`,
	} {
		var p modelsdev.Pricing
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		got := costcalc.PriceSnapshotFromPricing(p)
		want := costcalc.PriceSnapshotFromPricing(modelsdev.UnknownPricing())
		if got != want {
			t.Fatalf("guessed context tier: %+v", got)
		}
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAudioPricesRemainUnpricedDimensions(t *testing.T) {
	audio := 10.0
	p := modelsdev.Pricing{Input: 1, Output: 2, InputAudio: &audio}
	u := usageledger.NewUsage()
	u.Dims = map[string]usageledger.Component{"input_audio_tokens": {Tokens: 100, Provenance: usageledger.ProvenanceMeasured}}
	c := costcalc.Price(u, costcalc.PriceSnapshotFromPricing(p))
	if !c.Partial() || c.UnpricedTokens != 100 || c.Total() != 0 {
		t.Fatalf("audio silently priced: %+v", c)
	}
}
