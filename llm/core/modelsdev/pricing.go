package modelsdev

import (
	"encoding/json"
	"fmt"
	"math"
)

// PricingCurrency and PricingUnit describe every models.dev rate, including audio.
const PricingCurrency = "USD"
const PricingUnit = "per_million_tokens"

// Pricing preserves provider-specific rates and their availability. Numeric
// core fields remain source-compatible: Go literals treat every core rate as
// known (including zero). Use UnknownPricing to start with no known rates.
// JSON decoding records omitted/null rates in UnknownRates; marshaling omits
// them and retains explicit zero. Audio rates are nil when absent.
type Pricing struct {
	Input           float64       `json:"-"`
	Output          float64       `json:"-"`
	CacheWrite      float64       `json:"-"`
	CacheRead       float64       `json:"-"`
	Reasoning       float64       `json:"-"`
	UnknownRates    UnknownRates  `json:"-"`
	InputAudio      *float64      `json:"-"`
	OutputAudio     *float64      `json:"-"`
	ContextOver200K *Pricing      `json:"-"`
	Tiers           []PricingTier `json:"-"`
}

// UnknownRates marks the five core rates missing from a source.
type UnknownRates struct{ Input, Output, CacheWrite, CacheRead, Reasoning bool }

// UnknownPricing returns a price with every core rate unknown.
func UnknownPricing() Pricing {
	return Pricing{UnknownRates: UnknownRates{Input: true, Output: true, CacheWrite: true, CacheRead: true, Reasoning: true}}
}

// HasContextTiers reports whether choosing a rate requires request context.
func (p Pricing) HasContextTiers() bool { return len(p.Tiers) > 0 || p.ContextOver200K != nil }

// ContextTier identifies the upstream context threshold; Type is normally context.
type ContextTier struct {
	Type string `json:"type,omitempty"`
	Size int64  `json:"size"`
}

// PricingTier is a context-dependent set of rates, kept in upstream order.
// No automatic tier selection or fallback to base rates is implied.
type PricingTier struct {
	Pricing Pricing
	Tier    ContextTier
}

type pricingWire struct {
	Input           *float64      `json:"input,omitempty"`
	Output          *float64      `json:"output,omitempty"`
	CacheWrite      *float64      `json:"cache_write,omitempty"`
	CacheRead       *float64      `json:"cache_read,omitempty"`
	Reasoning       *float64      `json:"reasoning,omitempty"`
	InputAudio      *float64      `json:"input_audio,omitempty"`
	OutputAudio     *float64      `json:"output_audio,omitempty"`
	ContextOver200K *Pricing      `json:"context_over_200k,omitempty"`
	Tiers           []PricingTier `json:"tiers,omitempty"`
}

func (p Pricing) wire() pricingWire {
	rate := func(v float64, unknown bool) *float64 {
		if unknown {
			return nil
		}
		return &v
	}
	return pricingWire{
		Input: rate(p.Input, p.UnknownRates.Input), Output: rate(p.Output, p.UnknownRates.Output),
		CacheWrite: rate(p.CacheWrite, p.UnknownRates.CacheWrite), CacheRead: rate(p.CacheRead, p.UnknownRates.CacheRead),
		Reasoning: rate(p.Reasoning, p.UnknownRates.Reasoning), InputAudio: p.InputAudio, OutputAudio: p.OutputAudio,
		ContextOver200K: p.ContextOver200K, Tiers: p.Tiers,
	}
}

func (j pricingWire) pricing() (Pricing, error) {
	p := UnknownPricing()
	set := func(src *float64, dst *float64, unknown *bool) {
		*unknown = src == nil
		if src != nil {
			*dst = *src
		}
	}
	for name, rate := range map[string]*float64{
		"input": j.Input, "output": j.Output, "cache_write": j.CacheWrite,
		"cache_read": j.CacheRead, "reasoning": j.Reasoning,
		"input_audio": j.InputAudio, "output_audio": j.OutputAudio,
	} {
		if rate != nil && (*rate < 0 || math.IsNaN(*rate) || math.IsInf(*rate, 0)) {
			return Pricing{}, fmt.Errorf("modelsdev: invalid %s rate", name)
		}
	}
	set(j.Input, &p.Input, &p.UnknownRates.Input)
	set(j.Output, &p.Output, &p.UnknownRates.Output)
	set(j.CacheWrite, &p.CacheWrite, &p.UnknownRates.CacheWrite)
	set(j.CacheRead, &p.CacheRead, &p.UnknownRates.CacheRead)
	set(j.Reasoning, &p.Reasoning, &p.UnknownRates.Reasoning)
	p.InputAudio, p.OutputAudio = j.InputAudio, j.OutputAudio
	p.ContextOver200K, p.Tiers = j.ContextOver200K, j.Tiers
	return p, nil
}

func (p *Pricing) UnmarshalJSON(b []byte) error {
	var j pricingWire
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	next, err := j.pricing()
	if err != nil {
		return err
	}
	*p = next
	return nil
}

func (p Pricing) MarshalJSON() ([]byte, error) { return json.Marshal(p.wire()) }

func (p *PricingTier) UnmarshalJSON(b []byte) error {
	var j struct {
		pricingWire
		Tier ContextTier `json:"tier"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	price, err := j.pricing()
	if err != nil {
		return err
	}
	if j.Tier.Size < 0 {
		return fmt.Errorf("modelsdev: negative context tier size")
	}
	*p = PricingTier{Pricing: price, Tier: j.Tier}
	return nil
}

func (p PricingTier) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		pricingWire
		Tier ContextTier `json:"tier"`
	}{p.Pricing.wire(), p.Tier})
}
