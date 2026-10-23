package hitlrouting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Completion says how the asker is told the outcome.
type Completion string

const (
	// Wait blocks the asker, boundedly, for the outcome.
	Wait Completion = "wait"
	// Async gives the asker a handle and delivers the outcome later.
	Async Completion = "async"
	// Notify is a display only: nobody waits for an answer.
	Notify Completion = "notify"
)

// Delivery says how the host should get the request to the sink.
type Delivery string

const (
	// DurableMessage delivers the request as a durable message.
	DurableMessage Delivery = "durable_message"
	// Await holds the request for the asker to await.
	Await Delivery = "await"
)

// Limits on the free-form strings in a profile.
const (
	MaxRules   = 256
	MaxSinkLen = 128
	MaxIDLen   = 128
)

// Values is one match value or a list of them. In JSON it is a string or an array of
// strings. One value is exact, or a glob when it contains "*" or "?"; several values
// match when any equals the field exactly.
type Values []string

// UnmarshalJSON accepts a string or an array of strings.
func (v *Values) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*v = Values{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a string or an array of strings")
	}
	*v = Values(many)
	return nil
}

// MarshalJSON writes a single value as a string and several as an array.
func (v Values) MarshalJSON() ([]byte, error) {
	if len(v) == 1 {
		return json.Marshal(v[0])
	}
	return json.Marshal([]string(v))
}

// Match is the condition part of a rule. Every non-empty field must match.
type Match struct {
	Kind      Values `json:"kind,omitempty"`
	Source    Values `json:"source,omitempty"`
	Tool      Values `json:"tool,omitempty"`
	Namespace Values `json:"namespace,omitempty"`
}

type matchField struct {
	name   string
	values Values
}

// fields lists the four match fields in a fixed order.
func (m Match) fields() []matchField {
	return []matchField{{"kind", m.Kind}, {"source", m.Source}, {"tool", m.Tool}, {"namespace", m.Namespace}}
}

func (m Match) empty() bool {
	return len(m.Kind) == 0 && len(m.Source) == 0 && len(m.Tool) == 0 && len(m.Namespace) == 0
}

// Rule routes the requests it matches to a sink.
type Rule struct {
	ID         string     `json:"id"`
	Priority   int        `json:"priority,omitempty"` // higher first; ties keep file order
	Disabled   bool       `json:"disabled,omitempty"`
	Match      Match      `json:"match"`
	Sink       string     `json:"sink"`
	Completion Completion `json:"completion"`
	Delivery   Delivery   `json:"delivery"`
}

// CatchAll takes every request no rule matched. It is required.
type CatchAll struct {
	Sink       string     `json:"sink"`
	Completion Completion `json:"completion"`
	Delivery   Delivery   `json:"delivery"`
}

// Profile is the rule file.
type Profile struct {
	Version  string   `json:"version"`
	Rules    []Rule   `json:"rules,omitempty"`
	CatchAll CatchAll `json:"catch_all"`
}

// Parse decodes a profile strictly (an unknown field is an error, as is trailing data)
// and validates it.
func Parse(b []byte) (Profile, error) {
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("hitlrouting: %w", err)
	}
	if dec.More() {
		return Profile{}, errors.New("hitlrouting: unexpected data after the profile")
	}
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Validate reports every problem in the profile, joined.
func (p Profile) Validate() error {
	var errs []error
	add := func(f string, a ...any) { errs = append(errs, fmt.Errorf("hitlrouting: "+f, a...)) }
	if strings.TrimSpace(p.Version) == "" {
		add("version is required")
	}
	if len(p.Rules) > MaxRules {
		add("at most %d rules", MaxRules)
	}
	seen := map[string]bool{}
	for i, r := range p.Rules {
		switch {
		case r.ID == "" || len(r.ID) > MaxIDLen:
			add("rule #%d: id is required (1-%d characters)", i, MaxIDLen)
		case r.ID == catchAllID:
			add("rule #%d: id %q is reserved for the catch-all", i, catchAllID)
		case seen[r.ID]:
			add("rule %q: duplicate id", r.ID)
		}
		seen[r.ID] = true
		if r.Priority <= minPriority {
			add("rule %q: priority must be greater than %d", r.ID, minPriority)
		}
		if r.Match.empty() {
			add("rule %q: match names no field (kind, source, tool or namespace); a rule that matches everything is the catch_all", r.ID)
		}
		for _, f := range r.Match.fields() {
			checkValues(add, r.ID, f.name, f.values)
		}
		checkTarget(add, "rule "+fmt.Sprintf("%q", r.ID), r.Sink, r.Completion, r.Delivery)
	}
	checkTarget(add, "catch_all", p.CatchAll.Sink, p.CatchAll.Completion, p.CatchAll.Delivery)
	return errors.Join(errs...)
}

func checkValues(add func(string, ...any), rule, field string, vals Values) {
	for _, v := range vals {
		if v == "" {
			add("rule %q: %s has an empty value", rule, field)
		}
	}
	if len(vals) > 1 {
		for _, v := range vals {
			if strings.ContainsAny(v, "*?") {
				add("rule %q: %s lists %q; a glob is a single value, not a list member", rule, field, v)
			}
		}
	}
}

func checkTarget(add func(string, ...any), who, sink string, c Completion, d Delivery) {
	if sink == "" || len(sink) > MaxSinkLen || strings.ContainsAny(sink, " \t\r\n") {
		add("%s: sink is required (1-%d characters, no whitespace)", who, MaxSinkLen)
	}
	switch c {
	case Wait, Async, Notify:
	default:
		add("%s: completion %q is not one of wait, async, notify", who, c)
	}
	switch d {
	case DurableMessage, Await:
	default:
		add("%s: delivery %q is not one of durable_message, await", who, d)
	}
}
