package costcalc_test

import (
	"math"
	"testing"

	costcalc "github.com/hollis-labs/substrate/llm-core/costcalc"
	usageledger "github.com/hollis-labs/substrate/llm-core/usageledger"
)

// FuzzPrice checks that pricing never panics and stays finite for any
// non-negative token counts and any finite, non-negative rates up to a generous
// bound. Non-finite or negative rates are a caller error and out of contract.
func FuzzPrice(f *testing.F) {
	f.Add(int64(1000), int64(8000), int64(500), int64(300), int64(0), 3.0, 15.0, 3.75, 0.3, 0.0)
	f.Add(int64(math.MaxInt64), int64(0), int64(0), int64(math.MaxInt64), int64(1), 1e6, 1e6, 0.0, 0.0, 1e6)
	f.Fuzz(func(t *testing.T, in, cr, cw, out, rs int64, rIn, rOut, rCW, rCR, rRs float64) {
		for _, n := range []int64{in, cr, cw, out, rs} {
			if n < 0 {
				t.Skip()
			}
		}
		for _, r := range []float64{rIn, rOut, rCW, rCR, rRs} {
			if math.IsNaN(r) || math.IsInf(r, 0) || r < 0 || r > 1e6 {
				t.Skip()
			}
		}
		u := usageledger.NewUsage()
		m := usageledger.ProvenanceMeasured
		u.UncachedInputTokens = usageledger.Component{Tokens: in, Provenance: m}
		u.CacheReadTokens = usageledger.Component{Tokens: cr, Provenance: m}
		u.CacheWriteTokens = usageledger.Component{Tokens: cw, Provenance: m}
		u.OutputTokens = usageledger.Component{Tokens: out, Provenance: m}
		u.ReasoningTokens = usageledger.Component{Tokens: rs, Provenance: m}
		snap := usageledger.PriceSnapshot{InputPerMillion: rIn, OutputPerMillion: rOut, CacheWritePerMillion: rCW, CacheReadPerMillion: rCR, ReasoningPerMillion: rRs}
		total := costcalc.Price(u, snap).Total()
		if math.IsNaN(total) || math.IsInf(total, 0) || total < 0 {
			t.Fatalf("Total() = %v", total)
		}
	})
}
