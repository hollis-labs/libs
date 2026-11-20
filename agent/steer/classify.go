package steer

import (
	"fmt"
	"math"
)

// Facet is one question the classifier answers about a message.
type Facet string

// The facets steering acts on. A host may classify others; they are recorded, never applied.
const (
	// FacetSlot is the kind of work the message is about: a slot name.
	FacetSlot Facet = "slot"
	// FacetPreset is the mode that suits the message: a preset name.
	FacetPreset Facet = "preset"
	// FacetAct is what the message asks for (a question, a task, a correction).
	FacetAct Facet = "act"
	// FacetAffect is the owner's state of mind (rushed, stuck, exploring).
	FacetAffect Facet = "affect"
)

// FacetResult is the classifier's answer for one facet.
type FacetResult struct {
	// Label is the answer; "" with Abstained when the classifier had none.
	Label string `json:"label,omitempty"`
	// P is the classifier's confidence in Label, from 0 to 1.
	P float64 `json:"p"`
	// Abstained means the classifier declined to answer.
	Abstained bool `json:"abstained,omitempty"`
}

// Classification is the classifier's result for one message, per facet. Nil when no
// classifier ran.
type Classification map[Facet]FacetResult

// Gate says how far a facet's result may act.
type Gate string

const (
	// GateShadow records the result and does nothing else.
	GateShadow Gate = "shadow"
	// GateSuggest offers the result to the owner when it clears the threshold.
	GateSuggest Gate = "suggest"
	// GateAuto applies the result when it clears the threshold. Turn it on for a facet only
	// after replay shows enough precision.
	GateAuto Gate = "auto"
)

// FacetPolicy is one facet's gate and threshold.
type FacetPolicy struct {
	Gate Gate `json:"gate"`
	// Threshold is the confidence a result needs to be suggested or applied, in (0, 1].
	Threshold float64 `json:"threshold"`
}

// Policy is the classifier policy, per facet. A facet it does not name is shadowed.
type Policy map[Facet]FacetPolicy

// AutoFloor is the lowest threshold a facet may auto-apply at.
const AutoFloor = 0.5

// DefaultPolicy suggests slots and presets with good confidence and shadows the rest.
// Nothing applies on its own.
func DefaultPolicy() Policy {
	return Policy{
		FacetSlot:   {Gate: GateSuggest, Threshold: 0.7},
		FacetPreset: {Gate: GateSuggest, Threshold: 0.8},
		FacetAct:    {Gate: GateShadow, Threshold: 0.7},
		FacetAffect: {Gate: GateShadow, Threshold: 0.7},
	}
}

// Validate checks every facet's gate and threshold.
func (p Policy) Validate() error {
	for f, fp := range p {
		switch fp.Gate {
		case GateShadow, GateSuggest, GateAuto:
		default:
			return fmt.Errorf("classifier policy %s: gate %q: use shadow, suggest or auto", f, fp.Gate)
		}
		if math.IsNaN(fp.Threshold) || fp.Threshold <= 0 || fp.Threshold > 1 {
			return fmt.Errorf("classifier policy %s: threshold %v: use a number above 0, at most 1", f, fp.Threshold)
		}
		if fp.Gate == GateAuto && fp.Threshold < AutoFloor {
			return fmt.Errorf("classifier policy %s: an auto gate needs a threshold of at least %v", f, AutoFloor)
		}
	}
	return nil
}

// Outcome is what a policy makes of one facet's result.
type Outcome string

const (
	// OutcomeApply means the result is applied.
	OutcomeApply Outcome = "apply"
	// OutcomeSuggest means the result is offered to the owner, not applied.
	OutcomeSuggest Outcome = "suggest"
	// OutcomeRecord means the result is only recorded: shadowed, below the threshold,
	// abstained, or overruled.
	OutcomeRecord Outcome = "record"
)

// Verdict is the policy's ruling on one facet's result.
type Verdict struct {
	Facet   Facet   `json:"facet"`
	Label   string  `json:"label,omitempty"`
	P       float64 `json:"p"`
	Gate    Gate    `json:"gate"`
	Outcome Outcome `json:"outcome"`
	// Why explains a record outcome: "shadow", "below threshold", "abstained", "unknown
	// label", "deliberate mode", "pinned", "same as selected", "no input", "not acted on".
	Why string `json:"why,omitempty"`
}

// Judge rules on one facet's result. A facet the policy does not name, or names with
// an unknown gate, is shadowed.
func (p Policy) Judge(f Facet, r FacetResult) Verdict {
	fp, ok := p[f]
	if !ok || (fp.Gate != GateSuggest && fp.Gate != GateAuto) {
		fp = FacetPolicy{Gate: GateShadow, Threshold: 1}
	}
	v := Verdict{Facet: f, Label: r.Label, P: r.P, Gate: fp.Gate, Outcome: OutcomeRecord}
	switch {
	case r.Abstained || r.Label == "":
		v.Why = "abstained"
	case fp.Gate == GateShadow:
		v.Why = "shadow"
	case r.P < fp.Threshold:
		v.Why = "below threshold"
	case fp.Gate == GateAuto:
		v.Outcome = OutcomeApply
	default:
		v.Outcome = OutcomeSuggest
	}
	return v
}

// overrule turns an apply or suggest verdict into a recorded one, for the reason given.
func (v Verdict) overrule(why string) Verdict {
	if v.Outcome != OutcomeRecord {
		v.Outcome, v.Why = OutcomeRecord, why
	}
	return v
}

func (p Policy) clone() Policy {
	out := make(Policy, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
