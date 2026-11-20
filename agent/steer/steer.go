package steer

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/hollis-labs/libs/agent/core/reflexes"
)

// Steer runs the pipeline for a host. Build one with [New]; the zero Steer is not usable.
// It is safe for concurrent use as long as its selector is (the shipped one is).
type Steer struct {
	registry *Registry
	modes    *Modes
	selector Selector
	reflexes *Reflexes

	mu     sync.RWMutex
	policy Policy
}

// OwnerControls is what only the owner may do: set the mode and accept a suggestion. [New]
// returns it to the host, which keeps it out of anything the model or an outside party can
// reach. It is a capability, not an identity: it cannot be built, decoded or forged.
type OwnerControls struct {
	s *Steer
}

// ErrNotOwnerControls is returned when OwnerControls is decoded from data.
var ErrNotOwnerControls = errors.New("owner controls cannot be decoded")

// UnmarshalJSON refuses: owner controls never come from data.
func (*OwnerControls) UnmarshalJSON([]byte) error { return ErrNotOwnerControls }

// UnmarshalText refuses: owner controls never come from data.
func (*OwnerControls) UnmarshalText([]byte) error { return ErrNotOwnerControls }

// New returns a Steer over a registry, a selector and a reflex vocabulary, and the
// OwnerControls that alone can set its mode. Options set the classifier policy (validated),
// the clock, the cooldown store, the agent id and a reflex state fill.
func New(reg *Registry, sel Selector, vocab *Vocabulary, opts ...Option) (*Steer, *OwnerControls, error) {
	if reg == nil || sel == nil || vocab == nil {
		return nil, nil, errors.New("steer: a registry, a selector and a vocabulary are required")
	}
	o := collect(opts)
	if err := o.policy.Validate(); err != nil {
		return nil, nil, err
	}
	s := &Steer{registry: reg, modes: &Modes{reg: reg, now: o.now}, selector: sel, policy: o.policy,
		reflexes: &Reflexes{vocab: vocab, now: o.now, cooldowns: o.cooldowns, agentID: o.agentID, fill: o.fill}}
	return s, &OwnerControls{s: s}, nil
}

// Registry is the preset registry.
func (s *Steer) Registry() *Registry { return s.registry }

// Modes reads the deliberate mode.
func (s *Steer) Modes() *Modes { return s.modes }

// Selector is the work-slot selector.
func (s *Steer) Selector() Selector { return s.selector }

// Reflexes is the reflex runner, for host events and focus changes.
func (s *Steer) Reflexes() *Reflexes { return s.reflexes }

// Policy is a copy of the classifier policy.
func (s *Steer) Policy() Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy.clone()
}

// SetPolicy replaces the classifier policy after validating it.
func (s *Steer) SetPolicy(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = p.clone()
	return nil
}

// SetMode sets the deliberate mode; "" clears it.
func (c *OwnerControls) SetMode(name string) error { return c.s.modes.set(name) }

// Pinner is a selector that can pin a slot until the window in front changes.
type Pinner interface {
	Pin(name, view string)
}

// Accept applies a suggestion the owner accepted. It must be a suggest outcome for a label
// the host knows: a preset suggestion names a registered preset and becomes the deliberate
// mode; a slot suggestion names one of all and is pinned (the selector must be a
// [Pinner]). view is the view in front.
func (c *OwnerControls) Accept(v Verdict, view string, all []Slot) error {
	if v.Outcome != OutcomeSuggest {
		return errors.New("only a suggestion can be accepted")
	}
	switch v.Facet {
	case FacetPreset:
		return c.s.modes.set(v.Label)
	case FacetSlot:
		if _, ok := Named(all, v.Label); !ok {
			return errors.New("the suggested slot is not one the host offers")
		}
		p, ok := c.s.selector.(Pinner)
		if !ok {
			return errors.New("the selector cannot pin a slot")
		}
		p.Pin(v.Label, view)
		return nil
	}
	return errors.New("only slot and preset suggestions can be accepted")
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
	// Decision is the record of the turn's steering.
	Decision Decision `json:"decision"`
}

// Decision is what steering decided. It holds the situation as the host gave it, message
// text included; [Decision.TraceFields] is the prompt-free view for a trace.
type Decision struct {
	Situation Situation `json:"situation"`
	// Deliberate is the deliberate mode as set, even when its preset is no longer
	// registered and the default applied instead; "" when none is set.
	Deliberate string `json:"deliberate,omitempty"`
	// Mode is the preset in force; ModeSource is "deliberate", "classified" or "default".
	Mode       string `json:"mode"`
	ModeSource string `json:"mode_source"`
	// Why is the selector's reason for the slots.
	Why string `json:"why"`
	// Slots are the active slots, by name.
	Slots []string `json:"slots"`
	// Classified is the classifier's result; Verdicts are the policy's rulings on it.
	Classified Classification `json:"classified,omitempty"`
	Verdicts   []Verdict      `json:"verdicts,omitempty"`
	// Fired names the reflexes that fired: [] when none did, nil when no reflex layer ran.
	Fired []string `json:"fired"`
}

// TraceSituation is the prompt-free situation a trace records: no message text, no
// selection, no window titles, no host signals.
type TraceSituation struct {
	View   string `json:"view,omitempty"`
	Module string `json:"module,omitempty"`
	// Views are the open windows' views.
	Views      []string `json:"views,omitempty"`
	Mode       string   `json:"mode"`
	ModeSource string   `json:"mode_source"`
	// Deliberate is the deliberate mode as set, when it differs from Mode (its preset
	// is gone).
	Deliberate string   `json:"deliberate,omitempty"`
	Layout     string   `json:"layout,omitempty"`
	Source     string   `json:"source,omitempty"`
	Slots      []string `json:"slots"`
}

// TraceFields returns the decision in the shape of the libs agent/trace record's reserved
// fields: situation (a [TraceSituation]), classified (labels and confidences only; null
// when no classifier ran), why and fired. Nothing the owner wrote is included.
func (d Decision) TraceFields() (situation, classified json.RawMessage, why string, fired []string) {
	ts := TraceSituation{Mode: d.Mode, ModeSource: d.ModeSource, Layout: d.Situation.Layout, Source: d.Situation.Source, Slots: d.Slots}
	if d.Deliberate != d.Mode {
		ts.Deliberate = d.Deliberate
	}
	if f := d.Situation.Focused; f != nil {
		ts.View, ts.Module = f.View, f.Module
	}
	for _, w := range d.Situation.Windows {
		if w.View != "" && !slices.Contains(ts.Views, w.View) {
			ts.Views = append(ts.Views, w.View)
		}
	}
	if ts.Slots == nil {
		ts.Slots = []string{}
	}
	situation, _ = json.Marshal(ts)
	if d.Classified != nil {
		known := Classification{}
		for f, r := range d.Classified {
			if knownFacet(f) {
				if math.IsNaN(r.P) || r.P < 0 || r.P > 1 {
					r.P = 0
				}
				known[f] = r
			}
		}
		var verdicts []Verdict
		for _, v := range d.Verdicts {
			if knownFacet(v.Facet) {
				verdicts = append(verdicts, v)
			}
		}
		classified, _ = json.Marshal(struct {
			Result   Classification `json:"result"`
			Verdicts []Verdict      `json:"verdicts"`
		}{known, verdicts})
	}
	return situation, classified, d.Why, d.Fired
}

// Prepare runs the pipeline for one owner message. A host should call it before any routing
// tool runs: a halt answers the message, and nothing else should act on it. A reflex that
// is invalid, or cannot be evaluated or fired, is skipped and reported in the error; the
// rest of the turn is still returned.
func (s *Steer) Prepare(ctx context.Context, in Input) (Turn, error) {
	var t Turn
	var errs []error
	var valid []Reflex
	for _, r := range in.Reflexes {
		if err := r.Validate(s.reflexes.vocab); err != nil {
			errs = append(errs, err)
			continue
		}
		valid = append(valid, r)
	}
	policy := s.Policy()
	deliberate := s.modes.Current()
	t.Mode = Resolve(s.registry, deliberate, in.Classified, policy)
	sit := in.Sit
	sit.Mode = ""
	if t.Mode.Source != SourceDefault {
		sit.Mode = t.Mode.Preset.Name
	}

	t.Selection = s.selector.Select(ctx, sit, in.Slots)
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
		v := policy.Judge(FacetSlot, r)
		lead, known := Named(in.Slots, r.Label)
		switch {
		case v.Outcome == OutcomeRecord:
		case strings.TrimSpace(in.Sit.Input) == "":
			v = v.overrule("no input")
		case !known:
			v = v.overrule("unknown label")
		case t.Selection.Why == "pinned":
			v = v.overrule("pinned")
		case t.Selection.Primary().Name == r.Label:
			v = v.overrule("same as selected")
		case v.Outcome == OutcomeApply:
			t.Selection = leadWith(t.Selection, lead, "classified")
		}
		verdicts = append(verdicts, v)
	}
	for f, r := range in.Classified {
		if f != FacetSlot && f != FacetPreset {
			verdicts = append(verdicts, policy.Judge(f, r).overrule("not acted on"))
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

	fired, err := s.reflexes.OnMessage(ctx, Message{Text: in.Sit.Input, Prior: in.Prior, Sit: sit}, Active(valid, t.Mode.Preset))
	t.Fired = fired
	errs = append(errs, err)

	t.Decision = Decision{Situation: sit, Deliberate: deliberate, Mode: t.Mode.Preset.Name, ModeSource: t.Mode.Source, Why: t.Selection.Why,
		Slots: t.Selection.Names(), Classified: in.Classified, Verdicts: verdicts, Fired: t.Fired.Names}
	return t, errors.Join(errs...)
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
