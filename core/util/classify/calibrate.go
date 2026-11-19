package classify

import (
	"context"
	"errors"
	"math"
)

// Calibration is the result of FitTemperature.
type Calibration struct {
	Facet       string  `json:"facet"`
	Temperature float64 `json:"temperature"`
	// NLL is the mean negative log-likelihood of the gold labels at temperature
	// 1 (Before) and at the fitted temperature (After).
	Before float64 `json:"nll_before"`
	After  float64 `json:"nll_after"`
	N      int     `json:"n"`
}

// FitTemperature finds the temperature that makes a facet's probabilities best
// match how often it is right on labelled examples (the lowest mean negative
// log-likelihood), searching from 0.02 to 50. Examples a decisive vote settled
// are left out: no temperature changes them. Set the result as the facet's
// temperature in a pack layer. Fit on examples held out of training.
func FitTemperature(ctx context.Context, e *Engine, phase Phase, facet string, xs []Example) (Calibration, error) {
	f, ok := e.pack.facets[facet]
	if !ok {
		return Calibration{}, errors.New("classify: no facet " + facet)
	}
	type item struct {
		sums map[string]float64
		gold string
	}
	var items []item
	for _, x := range xs {
		gold := x.Labels[facet]
		if gold == "" || !f.has(gold) {
			continue
		}
		fr := e.Classify(ctx, Input{Text: x.Text, Context: x.Context, Phase: phase}).Facets[facet]
		if fr.Decided != "" {
			continue
		}
		sums := map[string]float64{}
		for _, v := range fr.Votes {
			sums[v.Label] += v.Weight
		}
		items = append(items, item{sums, gold})
	}
	if len(items) == 0 {
		return Calibration{}, errors.New("classify: no labelled examples to calibrate on")
	}
	nll := func(t float64) float64 {
		var total float64
		for _, it := range items {
			maxv := math.Inf(-1)
			for _, l := range f.Labels {
				maxv = math.Max(maxv, it.sums[l.Name]/t)
			}
			var z float64
			for _, l := range f.Labels {
				z += math.Exp(it.sums[l.Name]/t - maxv)
			}
			total -= it.sums[it.gold]/t - maxv - math.Log(z)
		}
		return total / float64(len(items))
	}
	// A log-spaced grid, then a golden-section search between the best point's
	// neighbours (the loss is unimodal in log t for a fixed set of sums).
	const lo, hi, steps = -1.7, 1.7, 340 // 10^-1.7 ≈ 0.02 .. 10^1.7 ≈ 50
	at := func(i int) float64 { return lo + (hi-lo)*float64(i)/steps }
	bi, bestNLL := -1, nll(1)
	for i := 0; i <= steps; i++ {
		if v := nll(math.Pow(10, at(i))); v < bestNLL-1e-12 {
			bi, bestNLL = i, v
		}
	}
	best := 1.0
	if bi >= 0 {
		a, b := at(max(bi-1, 0)), at(min(bi+1, steps))
		g := (math.Sqrt(5) - 1) / 2
		for range 60 {
			c, d := b-g*(b-a), a+g*(b-a)
			if nll(math.Pow(10, c)) < nll(math.Pow(10, d)) {
				b = d
			} else {
				a = c
			}
		}
		best = math.Pow(10, (a+b)/2)
		bestNLL = nll(best)
	}
	return Calibration{Facet: facet, Temperature: best, Before: nll(1), After: bestNLL, N: len(items)}, nil
}
