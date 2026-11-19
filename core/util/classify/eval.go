package classify

import (
	"context"
	"math"
	"sort"
)

// Example is a labelled input: the gold label per facet. A facet absent from
// Labels is not scored for that example.
type Example struct {
	Text    string            `json:"text"`
	Context map[string]string `json:"context,omitempty"`
	Labels  map[string]string `json:"labels"`
}

// Report is how an engine did on one facet.
type Report struct {
	Facet string `json:"facet"`
	N     int    `json:"n"`
	// Answered is how many it did not abstain on; Coverage is Answered/N.
	Answered int     `json:"answered"`
	Coverage float64 `json:"coverage"`
	// Precision is correct among answered; Accuracy is correct among all, an
	// abstention counting as wrong; TopAccuracy scores the top label even when
	// it abstained.
	Precision   float64 `json:"precision"`
	Accuracy    float64 `json:"accuracy"`
	TopAccuracy float64 `json:"top_accuracy"`
	// Majority is the accuracy of always answering the commonest gold label: the
	// floor a judge has to beat.
	Majority      float64 `json:"majority"`
	MajorityLabel string  `json:"majority_label"`
	// Bands are top-label accuracy by probability band.
	Bands []Band `json:"bands"`
	// Confusion counts gold -> answer ("" for an abstention).
	Confusion map[string]map[string]int `json:"confusion"`
	// Errors are the wrong or abstained examples, up to 50.
	Errors []Miss `json:"errors,omitempty"`
}

// Band is top-label accuracy for results with P in [Lo, Hi).
type Band struct {
	Lo       float64 `json:"lo"`
	Hi       float64 `json:"hi"`
	N        int     `json:"n"`
	Accuracy float64 `json:"accuracy"`
}

// Miss is one example the engine got wrong or abstained on.
type Miss struct {
	Text string  `json:"text"`
	Gold string  `json:"gold"`
	Got  string  `json:"got"`
	Top  string  `json:"top"`
	P    float64 `json:"p"`
}

var bandEdges = []float64{0, 0.5, 0.7, 0.9, 1.0000001}

// Evaluate classifies every example at phase and reports per facet, in pack
// order. It does not learn from the examples: hold them out of training.
func Evaluate(ctx context.Context, e *Engine, phase Phase, xs []Example) []Report {
	reps := map[string]*Report{}
	gold := map[string]map[string]int{}
	type band struct{ n, ok int }
	bands := map[string][]band{}
	for _, f := range e.pack.order {
		reps[f.Name] = &Report{Facet: f.Name, Confusion: map[string]map[string]int{}}
		gold[f.Name] = map[string]int{}
		bands[f.Name] = make([]band, len(bandEdges)-1)
	}
	var correctTop = map[string]int{}
	var correct = map[string]int{}
	for _, x := range xs {
		res := e.Classify(ctx, Input{Text: x.Text, Context: x.Context, Phase: phase})
		for facet, want := range x.Labels {
			r, ok := reps[facet]
			if !ok || want == "" {
				continue
			}
			fr := res.Facets[facet]
			r.N++
			gold[facet][want]++
			if r.Confusion[want] == nil {
				r.Confusion[want] = map[string]int{}
			}
			r.Confusion[want][fr.Label]++
			if !fr.Abstained {
				r.Answered++
			}
			if fr.Label == want {
				correct[facet]++
			}
			if fr.Top == want {
				correctTop[facet]++
			}
			for i := 0; i < len(bandEdges)-1; i++ {
				if fr.P >= bandEdges[i] && fr.P < bandEdges[i+1] {
					bands[facet][i].n++
					if fr.Top == want {
						bands[facet][i].ok++
					}
				}
			}
			if fr.Label != want && len(r.Errors) < 50 {
				r.Errors = append(r.Errors, Miss{Text: x.Text, Gold: want, Got: fr.Label, Top: fr.Top, P: fr.P})
			}
		}
	}
	var out []Report
	for _, f := range e.pack.order {
		r := reps[f.Name]
		if r.N > 0 {
			n := float64(r.N)
			r.Coverage = float64(r.Answered) / n
			r.Accuracy = float64(correct[f.Name]) / n
			r.TopAccuracy = float64(correctTop[f.Name]) / n
			if r.Answered > 0 {
				r.Precision = float64(correct[f.Name]) / float64(r.Answered)
			}
			labels := make([]string, 0, len(gold[f.Name]))
			for l := range gold[f.Name] {
				labels = append(labels, l)
			}
			sort.Strings(labels)
			for _, l := range labels {
				if c := gold[f.Name][l]; float64(c)/n > r.Majority {
					r.Majority, r.MajorityLabel = float64(c)/n, l
				}
			}
		}
		for i, b := range bands[f.Name] {
			bd := Band{Lo: bandEdges[i], Hi: math.Min(1, bandEdges[i+1]), N: b.n}
			if b.n > 0 {
				bd.Accuracy = float64(b.ok) / float64(b.n)
			}
			r.Bands = append(r.Bands, bd)
		}
		out = append(out, *r)
	}
	return out
}
