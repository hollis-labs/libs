package steer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/libs/agent/core/reflexes"
)

// The trigger kinds.
const (
	// TriggerText fires when the owner's message matches {"pattern": "<regexp>"}.
	TriggerText = "text"
	// TriggerPredicate fires on a predicate over the conversation (libs agent/core/reflexes).
	TriggerPredicate = "predicate"
	// TriggerEvent fires on a host event whose topic is {"topic": "<topic>"} or below it.
	TriggerEvent = "event"
	// TriggerFocus fires when a window of {"view": "<view>"} comes in front.
	TriggerFocus = "focus"
)

// The action kinds this package carries out itself.
const (
	// ActionRemind tells the agent {"body": "..."} with the next message.
	ActionRemind = "remind"
	// ActionHalt answers the message with {"message": "..."}; the agent is not called. Only
	// a text or predicate trigger may halt.
	ActionHalt = "halt"
)

// Reflex is one reflex: a standing rule, written as configuration.
type Reflex struct {
	Name        string          `json:"name"`
	About       string          `json:"about,omitempty"`
	TriggerKind string          `json:"trigger_kind"`
	Trigger     json.RawMessage `json:"trigger"`
	ActionKind  string          `json:"action_kind"`
	Action      json.RawMessage `json:"action"`
	// Cooldown is how long, in seconds, it stays quiet after firing. 0: no wait.
	Cooldown int `json:"cooldown_seconds,omitempty"`
	// Enabled switches it on everywhere. Off, it is on only while a preset lists it.
	Enabled bool `json:"enabled,omitempty"`
}

var reflexName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Vocabulary is the action kinds a host's reflexes may use, each with a check of its spec.
type Vocabulary struct {
	mu    sync.RWMutex
	kinds map[string]func(json.RawMessage) error
}

// NewVocabulary returns a vocabulary holding remind and halt.
func NewVocabulary() *Vocabulary {
	v := &Vocabulary{kinds: map[string]func(json.RawMessage) error{}}
	v.kinds[ActionRemind] = requireString("body")
	v.kinds[ActionHalt] = requireString("message")
	return v
}

// Register adds an action kind the host carries out. check validates a spec; nil accepts
// any JSON object. Registering remind or halt is refused.
func (v *Vocabulary) Register(kind string, check func(json.RawMessage) error) error {
	if kind == ActionRemind || kind == ActionHalt {
		return fmt.Errorf("action kind %q is built in", kind)
	}
	if !reflexName.MatchString(kind) {
		return fmt.Errorf("action kind %q: use lower case letters, digits, dots, dashes and underscores", kind)
	}
	if check == nil {
		check = func(raw json.RawMessage) error {
			if obj(raw) == nil {
				return errors.New("spec must be a JSON object")
			}
			return nil
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.kinds[kind] = check
	return nil
}

// Kinds lists the action kinds, sorted.
func (v *Vocabulary) Kinds() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]string, 0, len(v.kinds))
	for k := range v.kinds {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (v *Vocabulary) check(kind string) (func(json.RawMessage) error, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	c, ok := v.kinds[kind]
	return c, ok
}

func requireString(field string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		if s, _ := obj(raw)[field].(string); strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
}

func obj(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// Validate checks a reflex's shape against the vocabulary.
func (r Reflex) Validate(v *Vocabulary) error {
	if !reflexName.MatchString(r.Name) {
		return fmt.Errorf("reflex name %q: use lower case letters, digits, dots, dashes and underscores", r.Name)
	}
	if r.Cooldown < 0 {
		return fmt.Errorf("reflex %s: cooldown cannot be negative", r.Name)
	}
	t := obj(r.Trigger)
	switch r.TriggerKind {
	case TriggerText:
		p, _ := t["pattern"].(string)
		if _, err := regexp.Compile(p); p == "" || err != nil {
			return fmt.Errorf("reflex %s: text trigger: pattern must be a regular expression", r.Name)
		}
	case TriggerPredicate:
		if t == nil {
			return fmt.Errorf("reflex %s: predicate trigger: spec must be a JSON object", r.Name)
		}
		if _, err := reflexes.EvaluateTrigger("predicate", string(r.Trigger), reflexes.State{}); err != nil {
			return fmt.Errorf("reflex %s: predicate trigger: %w", r.Name, err)
		}
	case TriggerEvent:
		if s, _ := t["topic"].(string); s == "" {
			return fmt.Errorf("reflex %s: event trigger: topic is required", r.Name)
		}
	case TriggerFocus:
		if s, _ := t["view"].(string); s == "" {
			return fmt.Errorf("reflex %s: focus trigger: view is required", r.Name)
		}
	default:
		return fmt.Errorf("reflex %s: trigger kind %q: use text, predicate, event or focus", r.Name, r.TriggerKind)
	}
	check, ok := v.check(r.ActionKind)
	if !ok {
		return fmt.Errorf("reflex %s: action kind %q is not in the vocabulary (%s)", r.Name, r.ActionKind, strings.Join(v.Kinds(), ", "))
	}
	if err := check(r.Action); err != nil {
		return fmt.Errorf("reflex %s: %s: %w", r.Name, r.ActionKind, err)
	}
	if r.ActionKind == ActionHalt && r.TriggerKind != TriggerText && r.TriggerKind != TriggerPredicate {
		return fmt.Errorf("reflex %s: halt only answers a message; use a text or predicate trigger", r.Name)
	}
	return nil
}

// ParseReflexes decodes a JSON array of reflexes strictly and validates each. Names must be
// unique.
func ParseReflexes(raw []byte, v *Vocabulary) ([]Reflex, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var out []Reflex
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("reflexes: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range out {
		if err := r.Validate(v); err != nil {
			return nil, err
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("reflex %s is listed twice", r.Name)
		}
		seen[r.Name] = true
	}
	return out, nil
}

// Active is the reflexes that are on under a preset: those enabled everywhere, and those the
// preset lists.
func Active(all []Reflex, p Preset) []Reflex {
	var out []Reflex
	for _, r := range all {
		if r.Enabled || slices.Contains(p.Reflexes, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// Action is a fired reflex's action that the host carries out (any kind but remind and halt).
type Action struct {
	Reflex string          `json:"reflex"`
	Kind   string          `json:"kind"`
	Spec   json.RawMessage `json:"spec"`
}

// Fired is what the reflexes did.
type Fired struct {
	// Reminders are told to the agent with the message.
	Reminders []string `json:"reminders,omitempty"`
	// Halt, when set, answers the message; the agent is not called.
	Halt string `json:"halt,omitempty"`
	// Actions are for the host to carry out.
	Actions []Action `json:"actions,omitempty"`
	// Names are the reflexes that fired, in order.
	Names []string `json:"fired"`
}

// Message is a message the reflexes look at.
type Message struct {
	Text string
	// Prior is the recent conversation, most recent first.
	Prior []reflexes.MessageSignal
	Sit   Situation
}

// Reflexes runs reflexes and keeps their cooldowns. Reminders fired by an event or a focus
// change wait for the next message. It is safe for concurrent use.
type Reflexes struct {
	vocab *Vocabulary
	now   func() time.Time

	mu      sync.Mutex
	last    map[string]time.Time
	pending []string
}

// maxPending is how many reminders wait for the next message; older ones are dropped.
const maxPending = 10

// NewReflexes returns a runner using the vocabulary.
func NewReflexes(v *Vocabulary) *Reflexes {
	return &Reflexes{vocab: v, now: time.Now, last: map[string]time.Time{}}
}

// OnMessage runs the active text and predicate reflexes against a message. Reminders that
// were waiting come first.
func (x *Reflexes) OnMessage(_ context.Context, m Message, active []Reflex) (Fired, error) {
	var out Fired
	x.mu.Lock()
	out.Reminders, x.pending = x.pending, nil
	x.mu.Unlock()
	state := stateFor(m)
	var errs []error
	for _, r := range active {
		var fired bool
		switch r.TriggerKind {
		case TriggerText:
			var t struct {
				Pattern string `json:"pattern"`
			}
			_ = json.Unmarshal(r.Trigger, &t)
			re, err := regexp.Compile(t.Pattern)
			if err != nil {
				errs = append(errs, fmt.Errorf("reflex %s: %w", r.Name, err))
				continue
			}
			fired = re.MatchString(m.Text)
		case TriggerPredicate:
			ok, err := reflexes.EvaluateTrigger("predicate", string(r.Trigger), state)
			if err != nil {
				errs = append(errs, fmt.Errorf("reflex %s: %w", r.Name, err))
				continue
			}
			fired = ok
		default:
			continue
		}
		if fired && x.ready(r) {
			x.fire(r, &out)
		}
	}
	return out, errors.Join(errs...)
}

// OnEvent runs the active event reflexes for a host event. Reminders wait for the next
// message; other actions are returned for the host to carry out.
func (x *Reflexes) OnEvent(topic string, active []Reflex) Fired {
	return x.onHost(active, func(r Reflex) bool {
		t, _ := obj(r.Trigger)["topic"].(string)
		return r.TriggerKind == TriggerEvent && t != "" && (topic == t || strings.HasPrefix(topic, t+"."))
	})
}

// OnFocus runs the active focus reflexes when a window of view comes in front.
func (x *Reflexes) OnFocus(view string, active []Reflex) Fired {
	return x.onHost(active, func(r Reflex) bool {
		v, _ := obj(r.Trigger)["view"].(string)
		return r.TriggerKind == TriggerFocus && view != "" && view == v
	})
}

func (x *Reflexes) onHost(active []Reflex, match func(Reflex) bool) Fired {
	var out Fired
	for _, r := range active {
		if match(r) && x.ready(r) {
			x.fire(r, &out)
		}
	}
	if len(out.Reminders) > 0 {
		x.mu.Lock()
		x.pending = append(x.pending, out.Reminders...)
		if len(x.pending) > maxPending {
			x.pending = x.pending[len(x.pending)-maxPending:]
		}
		x.mu.Unlock()
		out.Reminders = nil
	}
	return out
}

// ready reports whether r is past its cooldown, and if so marks it fired now.
func (x *Reflexes) ready(r Reflex) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	now := x.now()
	if last, ok := x.last[r.Name]; ok && r.Cooldown > 0 && now.Sub(last) < time.Duration(r.Cooldown)*time.Second {
		return false
	}
	x.last[r.Name] = now
	return true
}

func (x *Reflexes) fire(r Reflex, out *Fired) {
	out.Names = append(out.Names, r.Name)
	a := obj(r.Action)
	switch r.ActionKind {
	case ActionRemind:
		if s, _ := a["body"].(string); s != "" {
			out.Reminders = append(out.Reminders, s)
		}
	case ActionHalt:
		if s, _ := a["message"].(string); s != "" && out.Halt == "" {
			out.Halt = s
		}
	default:
		out.Actions = append(out.Actions, Action{Reflex: r.Name, Kind: r.ActionKind, Spec: slices.Clone(r.Action)})
	}
}

// stateFor is the reflex library's view of a message: the conversation with the message
// first, and the situation as attributes (focused.view, focused.module, selection, mode,
// layout, source).
func stateFor(m Message) reflexes.State {
	now := reflexes.MessageSignal{Role: "user", Content: m.Text}
	st := reflexes.State{SessionID: "owner", Attrs: map[string]string{}}
	st.Messages = append([]reflexes.MessageSignal{now}, m.Prior...)
	st.UserMessages = []reflexes.MessageSignal{now}
	for _, p := range m.Prior {
		if p.Role == "user" {
			st.UserMessages = append(st.UserMessages, p)
		}
	}
	if f := m.Sit.Focused; f != nil {
		st.Attrs["focused.view"] = f.View
		st.Attrs["focused.module"] = f.Module
		st.Attrs["selection"] = map[bool]string{true: "yes", false: "no"}[strings.TrimSpace(f.Selection) != ""]
	}
	st.Attrs["mode"] = m.Sit.Mode
	st.Attrs["layout"] = m.Sit.Layout
	st.Attrs["source"] = m.Sit.Source
	return st
}
