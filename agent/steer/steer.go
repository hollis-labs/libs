package steer

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/hollis-labs/libs/agent/core/reflexes"
)

// Steer runs the pipeline for a host. Build one with [New]; it is safe for concurrent use
// as long as its parts are (the shipped ones are).
type Steer struct {
	Registry *Registry
	Modes    *Modes
	Selector Selector
	Policy   Policy
	Reflexes *Reflexes
}

// New returns a Steer over a registry, with a mode holder, the given selector, the default
// policy and a reflex runner over the vocabulary.
func New(reg *Registry, sel Selector, vocab *Vocabulary) *Steer {
	return &Steer{Registry: reg, Modes: NewModes(reg), Selector: sel, Policy: DefaultPolicy(), Reflexes: NewReflexes(vocab)}
}

// Input is what the host knows when the owner's message arrives.
type Input struct {
	Sit Situation
	// Slots are all the slots the host offers.
	Slots []Slot
	// Reflexes are all the reflexes the host holds; the pipeline picks the active ones.
	Reflexes []Reflex
	// Prior is the recent conversation, most recent first, for predicate reflexes.
	Prior []reflexes.MessageSignal
	// Classified is the classifier's result for the message; nil when none ran.
	Classified Classification
}

// Turn is what the pipeline decided for one message.
type Turn struct {
	Mode      Resolution `json:"mode"`
	Selection Selection  `json:"selection"`
	// Instructions are the standing instructions in force, in order: the preset's, then
	// each active slot's.
	Instructions []string `json:"instructions,omitempty"`
	// Tools are the tools to keep top of mind: the preset's, then the slots', without repeats.
	Tools []string `json:"tools,omitempty"`
	// Fired is what the reflexes did; Fired.Halt set means the host answers with it.
	Fired Fired `json:"fired"`
	// Suggestions are classified results the owner may accept (suggest gate).
	Suggestions []Verdict `json:"suggestions,omitempty"`
	// Decision is the record for the turn's trace.
	Decision Decision `json:"decision"`
}

// Decision is what a trace records about steering.
type Decision struct {
	// Situation is the situation the turn ran in, with the mode resolved.
	Situation Situation `json:"situation"`
	// Mode is the preset in force and its source.
	Mode string `json:"mode"`
	// ModeSource is "deliberate", "classified" or "default".
	ModeSource string `json:"mode_source"`
	// Why is the selector's reason for the slots.
	Why string `json:"why"`
	// Slots are the active slots, by name.
	Slots []string `json:"slots"`
	// Classified is the classifier's raw result; Verdicts are the policy's rulings on it.
	Classified Classification `json:"classified,omitempty"`
	Verdicts   []Verdict      `json:"verdicts,omitempty"`
	// Fired names the reflexes that fired: nil when the reflex layer did not run.
	Fired []string `json:"fired"`
}

// TraceFields returns the decision in the shape of the libs agent/trace record's reserved
// fields: situation, classified (null when no classifier ran), why and fired.
func (d Decision) TraceFields() (situation, classified json.RawMessage, why string, fired []string) {
	situation, _ = json.Marshal(struct {
		Situation
		ModeSource string   `json:"mode_source"`
		Slots      []string `json:"slots"`
	}{d.Situation, d.ModeSource, d.Slots})
	if d.Classified != nil {
		classified, _ = json.Marshal(struct {
			Result   Classification `json:"result"`
			Verdicts []Verdict      `json:"verdicts"`
		}{d.Classified, d.Verdicts})
	}
	return situation, classified, d.Why, d.Fired
}

// Prepare runs the pipeline for one owner message. A reflex trigger that cannot be
// evaluated is reported in the error; the rest of the turn is still returned.
func (s *Steer) Prepare(ctx context.Context, in Input) (Turn, error) {
	var t Turn
	t.Mode = Resolve(s.Registry, s.Modes.Current(), in.Classified, s.Policy)
	sit := in.Sit
	sit.Mode = t.Mode.Preset.Name

	t.Selection = s.Selector.Select(ctx, sit, in.Slots)
	for _, name := range t.Mode.Preset.Slots {
		if sl, ok := Named(in.Slots, name); ok && !t.Selection.has(name) {
			t.Selection.Slots = append(t.Selection.Slots, sl)
			if t.Selection.Why == "kept" || t.Selection.Why == "default" {
				t.Selection.Why = "preset"
			}
		}
	}

	var verdicts []Verdict
	if t.Mode.Verdict != nil {
		verdicts = append(verdicts, *t.Mode.Verdict)
	}
	if r, ok := in.Classified[FacetSlot]; ok {
		v := s.Policy.Judge(FacetSlot, r)
		lead, known := Named(in.Slots, r.Label)
		switch {
		case v.Outcome == OutcomeRecord:
		case !known:
			v = v.overrule("unknown label")
		case t.Selection.Why == "pinned":
			v = v.overrule("pinned")
		case t.Selection.Primary().Name == r.Label:
			v = v.overrule("same as selected")
		case v.Outcome == OutcomeApply && strings.TrimSpace(in.Sit.Input) != "":
			t.Selection = leadWith(t.Selection, lead, "classified")
		}
		verdicts = append(verdicts, v)
	}
	for f, r := range in.Classified {
		if f != FacetSlot && f != FacetPreset {
			verdicts = append(verdicts, s.Policy.Judge(f, r).overrule("not acted on"))
		}
	}
	slices.SortStableFunc(verdicts, func(a, b Verdict) int { return strings.Compare(string(a.Facet), string(b.Facet)) })
	for _, v := range verdicts {
		if v.Outcome == OutcomeSuggest {
			t.Suggestions = append(t.Suggestions, v)
		}
	}

	if p := strings.TrimSpace(t.Mode.Preset.Instructions); p != "" {
		t.Instructions = append(t.Instructions, p)
	}
	tools := slices.Clone(t.Mode.Preset.Tools)
	for _, sl := range t.Selection.Slots {
		if p := strings.TrimSpace(sl.Instructions); p != "" {
			t.Instructions = append(t.Instructions, p)
		}
		tools = append(tools, sl.Tools...)
	}
	t.Tools = dedupe(tools)

	var err error
	if s.Reflexes != nil {
		t.Fired, err = s.Reflexes.OnMessage(ctx, Message{Text: in.Sit.Input, Prior: in.Prior, Sit: sit}, Active(in.Reflexes, t.Mode.Preset))
		if t.Fired.Names == nil {
			t.Fired.Names = []string{}
		}
	}

	t.Decision = Decision{Situation: sit, Mode: t.Mode.Preset.Name, ModeSource: t.Mode.Source, Why: t.Selection.Why,
		Slots: t.Selection.Names(), Classified: in.Classified, Verdicts: verdicts, Fired: t.Fired.Names}
	if s.Reflexes == nil {
		t.Decision.Fired = nil
	}
	return t, err
}

// Accept applies a suggestion the owner accepted. A slot suggestion pins the slot (when the
// selector is a [FocusSelector]); a preset suggestion sets the deliberate mode. Only the
// owner accepts.
func (s *Steer) Accept(actor Actor, v Verdict, view string) error {
	if actor.Kind != ActorOwner {
		return ErrNotOwner
	}
	switch v.Facet {
	case FacetPreset:
		return s.Modes.Set(actor, v.Label)
	case FacetSlot:
		if fs, ok := s.Selector.(*FocusSelector); ok {
			fs.Pin(v.Label, view)
			return nil
		}
		return errors.New("the selector cannot pin a slot")
	}
	return errors.New("only slot and preset suggestions can be accepted")
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
