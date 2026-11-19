package classify

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"

	"github.com/hollis-labs/libs/core/util/decide"
)

// Decision is a judge that asks a decision provider (JEV through a gateway, or
// any decide.Decider) one Choice question per facet, built from the pack: the
// facet's About as the instructions, each label as an option described by its
// About and examples. The answers' probabilities become votes:
// Scale·ln(p·K) for K labels, so a label at the uniform 1/K votes 0, above it
// for, below it against, clamped to MaxWeight.
//
// The text is scrubbed of recognisable credentials before it leaves. Option
// order is rotated by a hash of the text, so a provider's lean toward the first
// option does not always favour the same label, and a replay asks the same
// question the same way.
type Decision struct {
	name    string
	decider decide.Decider
	pack    *Compiled
	facets  []string
	opts    DecisionOptions
}

// DecisionOptions tune a Decision judge.
type DecisionOptions struct {
	// Purpose is required: the gateway accounts spend per purpose.
	Purpose string
	// Model is the model hint; pin one to keep an evaluation reproducible.
	Model    string
	CallerID string
	// Scale converts ln(p·K) to log-odds; 0 is 1.
	Scale float64
	// MaxWeight clamps each vote; 0 is 4.
	MaxWeight float64
	// Examples is how many examples describe a label; 0 is 3, -1 is none.
	Examples int
	// Phase is when it may run; empty is Async.
	Phase Phase
}

// NewDecision returns a judge over facets of the pack (every facet when none
// are named).
func NewDecision(name string, d decide.Decider, pack *Compiled, o DecisionOptions, facets ...string) (*Decision, error) {
	if d == nil || pack == nil {
		return nil, errors.New("classify: decision judge needs a decider and a pack")
	}
	if strings.TrimSpace(o.Purpose) == "" {
		return nil, decide.ErrNoPurpose
	}
	if len(facets) == 0 {
		for _, f := range pack.order {
			facets = append(facets, f.Name)
		}
	}
	for _, f := range facets {
		if _, ok := pack.facets[f]; !ok {
			return nil, fmt.Errorf("classify: decision judge: no facet %q", f)
		}
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if o.MaxWeight == 0 {
		o.MaxWeight = 4
	}
	if o.Examples == 0 {
		o.Examples = 3
	}
	if !o.Phase.Valid() {
		o.Phase = Async
	}
	return &Decision{name: name, decider: d, pack: pack, facets: facets, opts: o}, nil
}

// Name implements Judge.
func (d *Decision) Name() string { return d.name }

// Phase implements Judge.
func (d *Decision) Phase() Phase { return d.opts.Phase }

// Version implements Versioned: the decider and the model hint.
func (d *Decision) Version() string { return d.decider.Name() + "@" + d.opts.Model }

// Request is the request the judge would send for in.
func (d *Decision) Request(in Input) decide.Request {
	text := decide.Scrub(in.Text)
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	rot := int(h.Sum32())
	req := decide.Request{
		State:    map[string]any{"text": text, "context": in.Context},
		Purpose:  d.opts.Purpose,
		Model:    d.opts.Model,
		CallerID: d.opts.CallerID,
	}
	for _, name := range d.facets {
		f := d.pack.facets[name]
		q := decide.Question{Key: f.Name, Type: decide.Choice, Instructions: f.About}
		if q.Instructions == "" {
			q.Instructions = "Which of these best describes the text?"
		}
		for _, l := range f.Labels {
			q.Options = append(q.Options, decide.Option{Key: l.Name, Description: d.describe(l)})
		}
		n := len(q.Options)
		r := rot % n
		q.Options = slices.Concat(q.Options[r:], q.Options[:r])
		req.Questions = append(req.Questions, q)
	}
	return req
}

func (d *Decision) describe(l LabelSpec) string {
	desc := l.About
	if desc == "" {
		desc = l.Name
	}
	if d.opts.Examples > 0 && len(l.Examples) > 0 {
		ex := l.Examples
		if len(ex) > d.opts.Examples {
			ex = ex[:d.opts.Examples]
		}
		desc += ` For example: "` + strings.Join(ex, `", "`) + `".`
	}
	return desc
}

// Judge implements Judge.
func (d *Decision) Judge(ctx context.Context, in Input) ([]Vote, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, nil
	}
	req := d.Request(in)
	resp, err := d.decider.Decide(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []Vote
	for _, q := range req.Questions {
		a, ok := resp.Answers[q.Key]
		if !ok || len(a.Probabilities) == 0 {
			continue
		}
		k := float64(len(q.Options))
		for _, o := range q.Options {
			p := math.Max(a.Probabilities[o.Key], 1e-4)
			w := math.Max(-d.opts.MaxWeight, math.Min(d.opts.MaxWeight, d.opts.Scale*math.Log(p*k)))
			out = append(out, Vote{Facet: q.Key, Label: o.Key, Weight: w,
				Evidence: fmt.Sprintf("%s p=%.3f", resp.Model, a.Probabilities[o.Key])})
		}
	}
	return out, nil
}
