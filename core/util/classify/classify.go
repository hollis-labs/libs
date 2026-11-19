package classify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Phase is how much time a classification may take. Phases are ordered:
// Keystroke < Submit < Async.
type Phase string

const (
	// Keystroke is while the owner types: microseconds, no I/O.
	Keystroke Phase = "keystroke"
	// Submit is when the text is sent: milliseconds, local models.
	Submit Phase = "submit"
	// Async is after the fact: seconds, model calls.
	Async Phase = "async"
)

func (p Phase) rank() int {
	switch p {
	case Keystroke:
		return 1
	case Submit:
		return 2
	case Async:
		return 3
	}
	return 0
}

// Valid reports whether p is one of the three phases.
func (p Phase) Valid() bool { return p.rank() > 0 }

// Input is what is classified. Context holds flat dotted fields, the same shape
// as a router subject's: "view", "module", "mode", "layout", "slot".
type Input struct {
	ID      string            `json:"id,omitempty"`
	Text    string            `json:"text"`
	Context map[string]string `json:"context,omitempty"`
	Phase   Phase             `json:"phase"`
}

// Vote is one judge's opinion on one facet. Weight is in log-odds units: 0 says
// nothing, 1 is a mild lean, 5 is near certainty. A negative weight counts
// against the label.
type Vote struct {
	Judge    string  `json:"judge"`
	Facet    string  `json:"facet"`
	Label    string  `json:"label"`
	Weight   float64 `json:"weight"`
	Decisive bool    `json:"decisive,omitempty"`
	Evidence string  `json:"evidence,omitempty"`
}

// Judge casts votes on an input. Implementations must be safe for concurrent
// use and must not block past ctx. An error drops the judge's votes for that
// input; it never fails the classification.
type Judge interface {
	Name() string
	// Phase is the cheapest phase the judge may run in.
	Phase() Phase
	Judge(ctx context.Context, in Input) ([]Vote, error)
}

// Versioned is implemented by a judge whose content changes while it runs (a
// model that learns). Its version is part of the engine's fingerprint at the
// moment of each classification.
type Versioned interface {
	Version() string
}

// FacetResult is the answer for one facet.
type FacetResult struct {
	Facet string `json:"facet"`
	// Label is the answer, or "" when the facet abstained.
	Label string `json:"label"`
	// Top is the most probable label, given even when the facet abstained.
	Top           string             `json:"top"`
	P             float64            `json:"p"`
	Margin        float64            `json:"margin"`
	Probabilities map[string]float64 `json:"probabilities"`
	Abstained     bool               `json:"abstained,omitempty"`
	// Decided names the judge whose decisive vote settled the facet.
	Decided string `json:"decided,omitempty"`
	Votes   []Vote `json:"votes,omitempty"`
}

// Result is one classification of one input at one phase.
type Result struct {
	InputID string                 `json:"input_id,omitempty"`
	Phase   Phase                  `json:"phase"`
	Pack    string                 `json:"pack"`
	Version string                 `json:"version"`
	Hash    string                 `json:"hash"`
	Facets  map[string]FacetResult `json:"facets"`
	// Failed names the judges that returned an error; their votes are absent.
	Failed  []string      `json:"failed,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Label is a facet's answer, "" when it abstained or is unknown.
func (r Result) Label(facet string) string { return r.Facets[facet].Label }

// Engine runs a compiled pack's judges and any added ones. Safe for concurrent
// use when its judges are.
type Engine struct {
	pack   *Compiled
	judges []Judge
	now    func() time.Time
}

// NewEngine returns an engine over a compiled pack. The pack's own rule judge
// runs first; extra judges run in the order given.
func NewEngine(c *Compiled, extra ...Judge) (*Engine, error) {
	if c == nil {
		return nil, errors.New("classify: nil pack")
	}
	e := &Engine{pack: c, judges: []Judge{c.Rules()}, now: time.Now}
	seen := map[string]bool{c.Rules().Name(): true}
	for _, j := range extra {
		if j == nil {
			return nil, errors.New("classify: nil judge")
		}
		if seen[j.Name()] {
			return nil, fmt.Errorf("classify: two judges named %q", j.Name())
		}
		if !j.Phase().Valid() {
			return nil, fmt.Errorf("classify: judge %q has no valid phase", j.Name())
		}
		seen[j.Name()] = true
		e.judges = append(e.judges, j)
	}
	return e, nil
}

// Pack is the engine's compiled pack.
func (e *Engine) Pack() *Compiled { return e.pack }

// Fingerprint is the hash of the pack's content and every judge's name and
// version, as of now.
func (e *Engine) Fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s|", len(e.pack.Hash), e.pack.Hash)
	for _, j := range e.judges {
		v := ""
		if vj, ok := j.(Versioned); ok {
			v = vj.Version()
		}
		fmt.Fprintf(h, "%d:%s|%d:%s|", len(j.Name()), j.Name(), len(v), v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Classify runs every judge whose phase is at or below in.Phase (Keystroke
// when unset) and combines their votes per facet.
func (e *Engine) Classify(ctx context.Context, in Input) Result {
	start := e.now()
	if !in.Phase.Valid() {
		in.Phase = Keystroke
	}
	res := Result{InputID: in.ID, Phase: in.Phase, Pack: e.pack.Name, Version: e.pack.Version,
		Hash: e.Fingerprint(), Facets: map[string]FacetResult{}}
	byFacet := map[string][]Vote{}
	for _, j := range e.judges {
		if j.Phase().rank() > in.Phase.rank() {
			continue
		}
		if ctx.Err() != nil {
			res.Failed = append(res.Failed, j.Name())
			continue
		}
		votes, err := j.Judge(ctx, in)
		if err != nil {
			res.Failed = append(res.Failed, j.Name())
			continue
		}
		for _, v := range votes {
			f, ok := e.pack.facets[v.Facet]
			if !ok || !f.has(v.Label) || math.IsNaN(v.Weight) || math.IsInf(v.Weight, 0) {
				continue // a vote for a facet or label the pack does not know says nothing
			}
			v.Judge = j.Name()
			byFacet[v.Facet] = append(byFacet[v.Facet], v)
		}
	}
	for _, f := range e.pack.order {
		res.Facets[f.Name] = combine(f, byFacet[f.Name])
	}
	res.Elapsed = e.now().Sub(start)
	return res
}

// combine adds the votes per label and softmaxes the sums. A decisive vote
// settles the facet; two decisive votes for different labels cancel each other
// and the sums decide.
func combine(f *facet, votes []Vote) FacetResult {
	out := FacetResult{Facet: f.Name, Votes: votes, Probabilities: map[string]float64{}}
	decisive := map[string]string{}
	for _, v := range votes {
		if v.Decisive && v.Weight > 0 {
			decisive[v.Label] = v.Judge
		}
	}
	if len(decisive) == 1 {
		for label, judge := range decisive {
			for _, l := range f.Labels {
				out.Probabilities[l.Name] = 0
			}
			out.Probabilities[label] = 1
			out.Label, out.Top, out.P, out.Margin, out.Decided = label, label, 1, 1, judge
		}
		return out
	}
	sums := make(map[string]float64, len(f.Labels))
	for _, v := range votes {
		sums[v.Label] += v.Weight
	}
	t := f.Temperature
	if t <= 0 {
		t = 1
	}
	maxv := math.Inf(-1)
	for _, l := range f.Labels {
		if s := sums[l.Name] / t; s > maxv {
			maxv = s
		}
	}
	var z float64
	for _, l := range f.Labels {
		p := math.Exp(sums[l.Name]/t - maxv)
		out.Probabilities[l.Name] = p
		z += p
	}
	type lp struct {
		label string
		p     float64
	}
	ranked := make([]lp, 0, len(f.Labels))
	for _, l := range f.Labels {
		p := out.Probabilities[l.Name] / z
		out.Probabilities[l.Name] = p
		ranked = append(ranked, lp{l.Name, p})
	}
	// Ties keep the pack's label order, so the answer is deterministic.
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].p > ranked[j].p })
	out.Top, out.P = ranked[0].label, ranked[0].p
	if len(ranked) > 1 {
		out.Margin = ranked[0].p - ranked[1].p
	} else {
		out.Margin = ranked[0].p
	}
	minP := f.MinP
	if l := f.label(out.Top); l != nil && l.MinP > minP {
		minP = l.MinP // a risky label asks for more
	}
	if len(votes) == 0 || out.P < minP || out.Margin < f.MinMargin {
		out.Abstained = true
		return out
	}
	out.Label = out.Top
	return out
}

// normalize lowercases and collapses whitespace; judges match on this form.
func normalize(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
