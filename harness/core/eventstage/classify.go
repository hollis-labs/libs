package eventstage

import (
	"context"
	"strings"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	mesh "github.com/hollis-labs/libs/mesh/core"
)

// Access reads and writes the parts of an envelope the Classify stage needs.
type Access[E any] struct {
	Kind      func(*E) string
	Payload   func(*E) []byte
	Sanitized func(*E) bool
	Get       func(*E) Fields
	Put       func(*E, Fields)
}

// Classify returns the Classify stage for rs. Rules see the payload only when
// the event is marked sanitized, so a Classify stage run on a raw event, or on
// one that failed closed, matches on kind, category and severity alone. The
// chain already runs Classify only after Sanitize; this is the second lock.
func Classify[E any](rs *RuleSet, acc Access[E]) Stage[E] {
	return classifyStage[E]{rs: rs, acc: acc}
}

type classifyStage[E any] struct {
	rs  *RuleSet
	acc Access[E]
}

func (c classifyStage[E]) Name() string { return "classify/" + c.rs.ID + "@" + c.rs.Version }

func (c classifyStage[E]) Apply(_ context.Context, e *E) (Outcome, error) {
	var payload []byte
	if c.acc.Sanitized(e) {
		payload = c.acc.Payload(e)
	}
	in := c.acc.Get(e)
	out, matched := c.rs.Evaluate(c.acc.Kind(e), in, payload)
	if len(matched) == 0 {
		return Outcome{}, nil
	}
	c.acc.Put(e, out)
	return Outcome{Changed: true, Note: strings.Join(matched, ",")}, nil
}

// RuntimeEventsClassify classifies runtimeevents.Event.
func RuntimeEventsClassify(rs *RuleSet) Stage[runtimeevents.Event] {
	return Classify(rs, Access[runtimeevents.Event]{
		Kind:      func(e *runtimeevents.Event) string { return string(e.Kind) },
		Payload:   func(e *runtimeevents.Event) []byte { return e.Payload },
		Sanitized: func(e *runtimeevents.Event) bool { return e.Class != nil && e.Class.Sanitized },
		Get: func(e *runtimeevents.Event) Fields {
			if e.Class == nil {
				return Fields{}
			}
			return Fields{e.Class.Category, e.Class.Severity, e.Class.Labels}
		},
		Put: func(e *runtimeevents.Event, f Fields) {
			if e.Class == nil {
				e.Class = &runtimeevents.Class{}
			}
			e.Class.Category, e.Class.Severity, e.Class.Labels = f.Category, f.Severity, f.Labels
		},
	})
}

// MeshClassify classifies mesh.Event.
func MeshClassify(rs *RuleSet) Stage[mesh.Event] {
	return Classify(rs, Access[mesh.Event]{
		Kind:      func(e *mesh.Event) string { return e.Kind },
		Payload:   func(e *mesh.Event) []byte { return e.Payload },
		Sanitized: func(e *mesh.Event) bool { return e.Class != nil && e.Class.Sanitized },
		Get: func(e *mesh.Event) Fields {
			if e.Class == nil {
				return Fields{}
			}
			return Fields{e.Class.Category, e.Class.Severity, e.Class.Labels}
		},
		Put: func(e *mesh.Event, f Fields) {
			if e.Class == nil {
				e.Class = &mesh.EventClass{}
			}
			e.Class.Category, e.Class.Severity, e.Class.Labels = f.Category, f.Severity, f.Labels
		},
	})
}
