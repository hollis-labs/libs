package hitl

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Outcome is the immutable terminal result of one item. It is a sealed union
// of exactly five variants: Resolved, Canceled, Expired, Failed and
// Superseded. Once an item has an Outcome, it never changes.
type Outcome interface {
	// OutcomeState returns the terminal state this variant belongs to.
	OutcomeState() State
	// OutcomeItemID returns the item the outcome belongs to.
	OutcomeItemID() string
	// OutcomeRevision returns the interaction revision that recorded it.
	OutcomeRevision() int64
	// OutcomeTime returns when the item became terminal.
	OutcomeTime() time.Time
	json.Marshaler
	isOutcome()
}

// Resolved is a participant's answer.
type Resolved struct {
	ItemID              string
	InteractionRevision int64
	Resolution          ResolutionRecord
}

// Canceled is a cancellation with its typed cause.
type Canceled struct {
	ItemID              string
	InteractionRevision int64
	Cause               CancelCause
	Reason              string
	TerminatedAt        time.Time
}

// Expired is a deadline passing. PolicyRef optionally names the policy that
// applied (Tangent writes "request.expires_at").
type Expired struct {
	ItemID              string
	InteractionRevision int64
	PolicyRef           string
	TerminatedAt        time.Time
}

// Failed is a terminal failure of the interaction itself.
type Failed struct {
	ItemID              string
	InteractionRevision int64
	ErrorCode           string
	Message             string
	TerminatedAt        time.Time
}

// Superseded points at the interaction that replaces this one.
type Superseded struct {
	ItemID              string
	InteractionRevision int64
	ReplacementItemID   string
	TerminatedAt        time.Time
}

var (
	_ Outcome = Resolved{}
	_ Outcome = Canceled{}
	_ Outcome = Expired{}
	_ Outcome = Failed{}
	_ Outcome = Superseded{}
)

func (Resolved) isOutcome()   {}
func (Canceled) isOutcome()   {}
func (Expired) isOutcome()    {}
func (Failed) isOutcome()     {}
func (Superseded) isOutcome() {}

// OutcomeState implements Outcome.
func (Resolved) OutcomeState() State { return StateResolved }

// OutcomeState implements Outcome.
func (Canceled) OutcomeState() State { return StateCanceled }

// OutcomeState implements Outcome.
func (Expired) OutcomeState() State { return StateExpired }

// OutcomeState implements Outcome.
func (Failed) OutcomeState() State { return StateFailed }

// OutcomeState implements Outcome.
func (Superseded) OutcomeState() State { return StateSuperseded }

// OutcomeItemID implements Outcome.
func (o Resolved) OutcomeItemID() string { return o.ItemID }

// OutcomeItemID implements Outcome.
func (o Canceled) OutcomeItemID() string { return o.ItemID }

// OutcomeItemID implements Outcome.
func (o Expired) OutcomeItemID() string { return o.ItemID }

// OutcomeItemID implements Outcome.
func (o Failed) OutcomeItemID() string { return o.ItemID }

// OutcomeItemID implements Outcome.
func (o Superseded) OutcomeItemID() string { return o.ItemID }

// OutcomeRevision implements Outcome.
func (o Resolved) OutcomeRevision() int64 { return o.InteractionRevision }

// OutcomeRevision implements Outcome.
func (o Canceled) OutcomeRevision() int64 { return o.InteractionRevision }

// OutcomeRevision implements Outcome.
func (o Expired) OutcomeRevision() int64 { return o.InteractionRevision }

// OutcomeRevision implements Outcome.
func (o Failed) OutcomeRevision() int64 { return o.InteractionRevision }

// OutcomeRevision implements Outcome.
func (o Superseded) OutcomeRevision() int64 { return o.InteractionRevision }

// OutcomeTime implements Outcome. For Resolved it is Resolution.ResolvedAt.
func (o Resolved) OutcomeTime() time.Time { return o.Resolution.ResolvedAt }

// OutcomeTime implements Outcome.
func (o Canceled) OutcomeTime() time.Time { return o.TerminatedAt }

// OutcomeTime implements Outcome.
func (o Expired) OutcomeTime() time.Time { return o.TerminatedAt }

// OutcomeTime implements Outcome.
func (o Failed) OutcomeTime() time.Time { return o.TerminatedAt }

// OutcomeTime implements Outcome.
func (o Superseded) OutcomeTime() time.Time { return o.TerminatedAt }

type outcomeWire struct {
	ContractVersion     string            `json:"contract_version"`
	State               State             `json:"state"`
	ItemID              string            `json:"item_id"`
	InteractionRevision int64             `json:"interaction_revision"`
	Resolution          *ResolutionRecord `json:"resolution,omitempty"`
	Cause               CancelCause       `json:"cause,omitempty"`
	Reason              string            `json:"reason,omitempty"`
	PolicyRef           string            `json:"policy_ref,omitempty"`
	ErrorCode           string            `json:"error_code,omitempty"`
	Message             string            `json:"message,omitempty"`
	ReplacementItemID   string            `json:"replacement_item_id,omitempty"`
	TerminatedAt        *time.Time        `json:"terminated_at,omitempty"`
}

// MarshalJSON encodes the wire form (contract_version and state included).
func (o Resolved) MarshalJSON() ([]byte, error) {
	res := o.Resolution
	return json.Marshal(outcomeWire{
		ContractVersion: ContractVersion, State: StateResolved, ItemID: o.ItemID,
		InteractionRevision: o.InteractionRevision, Resolution: &res,
	})
}

// MarshalJSON encodes the wire form.
func (o Canceled) MarshalJSON() ([]byte, error) {
	t := o.TerminatedAt
	return json.Marshal(outcomeWire{
		ContractVersion: ContractVersion, State: StateCanceled, ItemID: o.ItemID,
		InteractionRevision: o.InteractionRevision, Cause: o.Cause, Reason: o.Reason, TerminatedAt: &t,
	})
}

// MarshalJSON encodes the wire form.
func (o Expired) MarshalJSON() ([]byte, error) {
	t := o.TerminatedAt
	return json.Marshal(outcomeWire{
		ContractVersion: ContractVersion, State: StateExpired, ItemID: o.ItemID,
		InteractionRevision: o.InteractionRevision, PolicyRef: o.PolicyRef, TerminatedAt: &t,
	})
}

// MarshalJSON encodes the wire form.
func (o Failed) MarshalJSON() ([]byte, error) {
	t := o.TerminatedAt
	return json.Marshal(outcomeWire{
		ContractVersion: ContractVersion, State: StateFailed, ItemID: o.ItemID,
		InteractionRevision: o.InteractionRevision, ErrorCode: o.ErrorCode, Message: o.Message, TerminatedAt: &t,
	})
}

// MarshalJSON encodes the wire form.
func (o Superseded) MarshalJSON() ([]byte, error) {
	t := o.TerminatedAt
	return json.Marshal(outcomeWire{
		ContractVersion: ContractVersion, State: StateSuperseded, ItemID: o.ItemID,
		InteractionRevision: o.InteractionRevision, ReplacementItemID: o.ReplacementItemID, TerminatedAt: &t,
	})
}

// ErrInvalidOutcome is wrapped by every UnmarshalOutcome failure.
var ErrInvalidOutcome = errors.New("hitl: invalid terminal outcome")

// UnmarshalOutcome decodes a terminal outcome, discriminating on "state".
// Unknown members are ignored; missing required members and non-terminal or
// unknown states are errors wrapping ErrInvalidOutcome.
func UnmarshalOutcome(data []byte) (Outcome, error) {
	var w outcomeWire
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidOutcome, err)
	}
	if w.ItemID == "" {
		return nil, fmt.Errorf("%w: item_id is required", ErrInvalidOutcome)
	}
	if w.InteractionRevision < 1 {
		return nil, fmt.Errorf("%w: interaction_revision must be at least 1", ErrInvalidOutcome)
	}
	needTime := func() (time.Time, error) {
		if w.TerminatedAt == nil {
			return time.Time{}, fmt.Errorf("%w: terminated_at is required for state %q", ErrInvalidOutcome, w.State)
		}
		return *w.TerminatedAt, nil
	}
	switch w.State {
	case StateResolved:
		if w.Resolution == nil {
			return nil, fmt.Errorf("%w: resolution is required for state %q", ErrInvalidOutcome, w.State)
		}
		res := w.Resolution
		if res.ResolutionID == "" || res.Response.validate() != nil || res.Participant.Validate() != nil ||
			res.ResolvedAt.IsZero() || res.InteractionRevision < 1 {
			return nil, fmt.Errorf("%w: malformed resolution", ErrInvalidOutcome)
		}
		return Resolved{ItemID: w.ItemID, InteractionRevision: w.InteractionRevision, Resolution: *w.Resolution}, nil
	case StateCanceled:
		t, err := needTime()
		if err != nil {
			return nil, err
		}
		if !w.Cause.Valid() {
			return nil, fmt.Errorf("%w: unknown cancel cause %q", ErrInvalidOutcome, w.Cause)
		}
		return Canceled{ItemID: w.ItemID, InteractionRevision: w.InteractionRevision, Cause: w.Cause, Reason: w.Reason, TerminatedAt: t}, nil
	case StateExpired:
		t, err := needTime()
		if err != nil {
			return nil, err
		}
		return Expired{ItemID: w.ItemID, InteractionRevision: w.InteractionRevision, PolicyRef: w.PolicyRef, TerminatedAt: t}, nil
	case StateFailed:
		t, err := needTime()
		if err != nil {
			return nil, err
		}
		if w.ErrorCode == "" || w.Message == "" {
			return nil, fmt.Errorf("%w: error_code and message are required for state %q", ErrInvalidOutcome, w.State)
		}
		return Failed{ItemID: w.ItemID, InteractionRevision: w.InteractionRevision, ErrorCode: w.ErrorCode, Message: w.Message, TerminatedAt: t}, nil
	case StateSuperseded:
		t, err := needTime()
		if err != nil {
			return nil, err
		}
		if w.ReplacementItemID == "" {
			return nil, fmt.Errorf("%w: replacement_item_id is required for state %q", ErrInvalidOutcome, w.State)
		}
		return Superseded{ItemID: w.ItemID, InteractionRevision: w.InteractionRevision, ReplacementItemID: w.ReplacementItemID, TerminatedAt: t}, nil
	case StateSubmitted, StateValidated, StateStaged, StatePresented, StateInProgress:
		return nil, fmt.Errorf("%w: state %q is not terminal", ErrInvalidOutcome, w.State)
	}
	return nil, fmt.Errorf("%w: unknown state %q", ErrInvalidOutcome, w.State)
}
