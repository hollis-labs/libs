package gate

import (
	"bytes"
	"encoding/json"

	"github.com/hollis-labs/libs/mesh/core/hitl"
)

// Validate checks the discriminant and coherent authoritative projection. It
// does not authenticate the outcome or replace AuthorityVerifier at admission.
func (r Resolution) Validate() error {
	if err := r.Ticket.Validate(); err != nil {
		return err
	}
	if r.State == Pending {
		if r.Decision != nil || r.Outcome != nil {
			return invalid("pending resolution has terminal data")
		}
		return nil
	}
	if r.Outcome == nil {
		return invalid("terminal HITL outcome required")
	}
	raw, err := json.Marshal(r.Outcome)
	if err != nil {
		return err
	}
	outcome, err := hitl.UnmarshalOutcome(raw)
	if err != nil {
		return err
	}
	if outcome.OutcomeItemID() != r.Ticket.Handle.ItemID || outcome.OutcomeRevision() < r.Ticket.Handle.Revision || outcome.OutcomeTime().IsZero() {
		return invalid("outcome does not match ticket")
	}
	switch r.State {
	case Decided:
		resolved, ok := outcome.(hitl.Resolved)
		if !ok || r.Decision == nil {
			return invalid("decided resolution needs HITL resolved outcome and decision")
		}
		d := r.Decision
		if err := d.Ticket.Validate(); err != nil {
			return err
		}
		decisionBytes, err := json.Marshal(d.Resolution)
		if err != nil {
			return err
		}
		resolutionBytes, err := json.Marshal(resolved.Resolution)
		if err != nil {
			return err
		}
		if d.Ticket != r.Ticket || !bytes.Equal(decisionBytes, resolutionBytes) {
			return invalid("decision differs from authoritative projection")
		}
	case Expired:
		if outcome.OutcomeState() != hitl.StateExpired || r.Decision != nil {
			return invalid("expired resolution discriminant")
		}
	case Cancelled:
		if outcome.OutcomeState() != hitl.StateCanceled || r.Decision != nil {
			return invalid("cancelled resolution discriminant")
		}
	default:
		return invalid("unknown resolution state")
	}
	return nil
}

type resolutionWire struct {
	State    ResolutionState `json:"state"`
	Ticket   Ticket          `json:"ticket"`
	Decision *Decision       `json:"decision,omitempty"`
	Outcome  json.RawMessage `json:"outcome,omitempty"`
}

func (r Resolution) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	w := resolutionWire{State: r.State, Ticket: r.Ticket, Decision: r.Decision}
	if r.Outcome != nil {
		b, err := json.Marshal(r.Outcome)
		if err != nil {
			return nil, err
		}
		w.Outcome = b
	}
	return json.Marshal(w)
}
func (r *Resolution) UnmarshalJSON(b []byte) error {
	var w resolutionWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	next := Resolution{State: w.State, Ticket: w.Ticket, Decision: w.Decision}
	if len(w.Outcome) > 0 {
		out, err := hitl.UnmarshalOutcome(w.Outcome)
		if err != nil {
			return err
		}
		next.Outcome = out
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
