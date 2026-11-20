package desk

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// WorkKind says how a piece of work gets done. The host maps each kind to
// a way of running it (a subtask variant, a council, mail to a seat).
type WorkKind string

const (
	// KindAnswer is answered in the conversation by the chat agent. A plan
	// of a single answer item means the front desk has nothing to add.
	KindAnswer WorkKind = "answer"
	// KindResearch finds things out; it fans out well.
	KindResearch WorkKind = "research"
	// KindAnalysis reasons over material already at hand.
	KindAnalysis WorkKind = "analysis"
	// KindCouncil asks several perspectives and synthesizes them.
	KindCouncil WorkKind = "council"
	// KindDelegate hands a change to the seat that owns the system, with
	// acceptance criteria (mail or a tracked task).
	KindDelegate WorkKind = "delegate"
	// KindQuestion needs the owner before anything else can go on.
	KindQuestion WorkKind = "question"
)

// Kinds lists the known work kinds.
func Kinds() []WorkKind {
	return []WorkKind{KindAnswer, KindResearch, KindAnalysis, KindCouncil, KindDelegate, KindQuestion}
}

func (k WorkKind) valid() bool { return slices.Contains(Kinds(), k) }

// PlanItem is one piece of work in a plan.
type PlanItem struct {
	ID        string   `json:"id"`
	Kind      WorkKind `json:"kind"`
	Goal      string   `json:"goal"`             // what to do, standing on its own
	Expect    string   `json:"expect,omitempty"` // the outcome that counts as done
	DependsOn []string `json:"depends_on,omitempty"`
	Target    string   `json:"target,omitempty"`   // a variant, seat or tool, when the plan names one
	Delivery  Action   `json:"delivery,omitempty"` // how its result should reach the owner; empty leaves it to the policy
}

// Plan is the front desk's reading of one owner message: the work it
// splits into and the topic it belongs to.
type Plan struct {
	Message    string     `json:"message"`
	Mode       string     `json:"mode,omitempty"`
	Topic      TopicID    `json:"topic,omitempty"`       // an open topic this continues
	TopicTitle string     `json:"topic_title,omitempty"` // a new topic's title
	Items      []PlanItem `json:"items"`
	Confidence float64    `json:"confidence"`
	Why        string     `json:"why,omitempty"`
	Source     string     `json:"source"` // "rules" or "model"
}

// Trivial reports whether the plan adds nothing to a normal chat turn: one
// answer item.
func (p Plan) Trivial() bool {
	return len(p.Items) == 1 && p.Items[0].Kind == KindAnswer
}

// Validate checks that ids are present and unique, kinds and delivery
// classes are known, dependencies exist, and there is no cycle.
func (p Plan) Validate() error {
	if len(p.Items) == 0 {
		return errors.New("desk: plan has no items")
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("desk: plan confidence %v is outside [0,1]", p.Confidence)
	}
	seen := map[string]bool{}
	var errs []error
	for _, it := range p.Items {
		switch {
		case it.ID == "":
			errs = append(errs, errors.New("desk: plan item without an id"))
		case seen[it.ID]:
			errs = append(errs, fmt.Errorf("desk: plan item id %q repeats", it.ID))
		}
		seen[it.ID] = true
		if !it.Kind.valid() {
			errs = append(errs, fmt.Errorf("desk: plan item %q has unknown kind %q", it.ID, it.Kind))
		}
		if strings.TrimSpace(it.Goal) == "" {
			errs = append(errs, fmt.Errorf("desk: plan item %q has no goal", it.ID))
		}
		if it.Delivery != "" && !it.Delivery.valid() {
			errs = append(errs, fmt.Errorf("desk: plan item %q has unknown delivery %q", it.ID, it.Delivery))
		}
	}
	for _, it := range p.Items {
		for _, d := range it.DependsOn {
			if !seen[d] {
				errs = append(errs, fmt.Errorf("desk: plan item %q depends on unknown %q", it.ID, d))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if _, err := p.Waves(); err != nil {
		return err
	}
	return nil
}

// Waves orders the items for running: each wave's items depend only on
// items in earlier waves, so a wave runs in parallel. Items keep plan order
// within a wave.
func (p Plan) Waves() ([][]PlanItem, error) {
	done := map[string]bool{}
	left := slices.Clone(p.Items)
	var waves [][]PlanItem
	for len(left) > 0 {
		var wave, rest []PlanItem
		for _, it := range left {
			ready := true
			for _, d := range it.DependsOn {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, it)
			} else {
				rest = append(rest, it)
			}
		}
		if len(wave) == 0 {
			ids := make([]string, len(rest))
			for i, it := range rest {
				ids[i] = it.ID
			}
			return nil, fmt.Errorf("desk: plan items %s depend on each other in a cycle", strings.Join(ids, ", "))
		}
		for _, it := range wave {
			done[it.ID] = true
		}
		waves = append(waves, wave)
		left = rest
	}
	return waves, nil
}

// Card is a short plain-text rendering of the plan for the owner, as in
// "doing these 3 in parallel".
func (p Plan) Card() string {
	var b strings.Builder
	waves, err := p.Waves()
	if err != nil {
		waves = [][]PlanItem{p.Items}
	}
	switch {
	case len(p.Items) == 1:
		b.WriteString("Plan: 1 item")
	case len(waves) == 1:
		fmt.Fprintf(&b, "Plan: %d items in parallel", len(p.Items))
	default:
		fmt.Fprintf(&b, "Plan: %d items in %d steps", len(p.Items), len(waves))
	}
	if p.TopicTitle != "" {
		fmt.Fprintf(&b, " (topic: %s)", p.TopicTitle)
	}
	b.WriteString("\n")
	for i, wave := range waves {
		for _, it := range wave {
			prefix := "-"
			if len(waves) > 1 {
				prefix = fmt.Sprintf("%d.", i+1)
			}
			fmt.Fprintf(&b, "%s [%s] %s", prefix, it.Kind, it.Goal)
			if it.Target != "" {
				fmt.Fprintf(&b, " → %s", it.Target)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
