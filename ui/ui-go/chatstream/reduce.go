package chatstream

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"
)

// RunStatus is where a run stands.
type RunStatus string

// The run statuses.
const (
	StatusStreaming RunStatus = "streaming"
	StatusFinished  RunStatus = "finished"
	StatusErrored   RunStatus = "errored"
	StatusAborted   RunStatus = "aborted"
)

// Done reports whether the run has ended.
func (s RunStatus) Done() bool {
	return s == StatusFinished || s == StatusErrored || s == StatusAborted
}

// Part is one piece of a message, built from a part.start, its deltas and its
// part.end.
type Part struct {
	ID        string                     `json:"id"`
	MessageID string                     `json:"message_id,omitempty"`
	Kind      PartKind                   `json:"kind"`
	Meta      map[string]json.RawMessage `json:"meta,omitempty"`
	// Text is the accumulated text of a text, reasoning or refusal part.
	Text string `json:"text,omitempty"`
	// Args is the accumulated JSON fragments of a tool_call part. Final is the
	// part.end's final value: a tool call's parsed arguments, a reasoning
	// part's signature or encrypted value.
	Args  string          `json:"args,omitempty"`
	Final json.RawMessage `json:"final,omitempty"`
	// Open is true until the part.end arrives.
	Open bool `json:"open,omitempty"`
}

// Arguments returns a tool_call part's arguments: Final if the part ended with
// one, else the accumulated fragments when they form valid JSON, else nil.
func (p Part) Arguments() json.RawMessage {
	if len(p.Final) > 0 {
		return p.Final
	}
	if p.Args != "" && json.Valid([]byte(p.Args)) {
		return json.RawMessage(p.Args)
	}
	return nil
}

// Approval is an approval.request as it stands.
type Approval struct {
	ID         string          `json:"id"`
	CallID     string          `json:"call_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	Mode       ApprovalMode    `json:"mode,omitempty"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
}

// Step is one model call inside the run.
type Step struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Open bool   `json:"open,omitempty"`
}

// Activity is an activity event as received.
type Activity struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value,omitempty"`
	Patch json.RawMessage `json:"patch,omitempty"`
}

// Gap is a gap event as received.
type Gap struct {
	From   uint64 `json:"from,omitempty"`
	To     uint64 `json:"to,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RunError is how a run failed.
type RunError struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Message is what a run's events reduce to: the normalized assistant message
// plus the run's status. It is the snapshot a client that cannot replay
// loads, and it is JSON-serializable so it can be stored.
type Message struct {
	RunID    string `json:"run_id"`
	ID       string `json:"id,omitempty"`
	Role     string `json:"role,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	Status       RunStatus    `json:"status"`
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	RawReason    string       `json:"raw_reason,omitempty"`
	AbortReason  string       `json:"abort_reason,omitempty"`
	Error        *RunError    `json:"error,omitempty"`
	Usage        *Usage       `json:"usage,omitempty"`

	Parts      []Part     `json:"parts,omitempty"`
	Steps      []Step     `json:"steps,omitempty"`
	Approvals  []Approval `json:"approvals,omitempty"`
	Activities []Activity `json:"activities,omitempty"`
	Raws       []Raw      `json:"raws,omitempty"`
	Gaps       []Gap      `json:"gaps,omitempty"`

	// Incomplete is true once a gap was seen: what precedes it may be missing
	// events, so the message may be missing content.
	Incomplete bool `json:"incomplete,omitempty"`
	// LastSeq is the highest Seq applied. Events at or below it are skipped when
	// applied again, so replaying an overlapping range is safe.
	LastSeq uint64 `json:"last_seq,omitempty"`

	// Started, MessageOpen and CurrentMessage are the reducer's own state, kept
	// exported so that a stored snapshot can be resumed with Reduce.
	Started        bool   `json:"started,omitempty"`
	MessageOpen    bool   `json:"message_open,omitempty"`
	CurrentMessage string `json:"current_message,omitempty"`
}

// Text returns the concatenated text of the message's text parts.
func (m *Message) Text() string {
	var out string
	for _, p := range m.Parts {
		if p.Kind == PartText {
			out += p.Text
		}
	}
	return out
}

// Clone returns a deep copy of m.
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}
	c := *m
	if m.Error != nil {
		e := *m.Error
		c.Error = &e
	}
	if m.Usage != nil {
		u := *m.Usage
		u.Extra = maps.Clone(m.Usage.Extra)
		c.Usage = &u
	}
	c.Parts = make([]Part, len(m.Parts))
	for i, p := range m.Parts {
		p.Meta = cloneRawMap(p.Meta)
		p.Final = append(json.RawMessage(nil), p.Final...)
		c.Parts[i] = p
	}
	c.Steps = append([]Step(nil), m.Steps...)
	c.Approvals = append([]Approval(nil), m.Approvals...)
	c.Activities = append([]Activity(nil), m.Activities...)
	c.Raws = append([]Raw(nil), m.Raws...)
	c.Gaps = append([]Gap(nil), m.Gaps...)
	if len(m.Parts) == 0 {
		c.Parts = nil
	}
	return &c
}

func cloneRawMap(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

// Reduce folds events into a Message. It is pure: prior (nil to start) is not
// modified, and the same events always give the same result. Applying
// events[k:] to the result of applying events[:k] gives the same Message as
// applying all of them (replay from any cursor equals full replay), and
// events at or below prior.LastSeq are skipped, so an overlapping replay is
// harmless. An unsequenced event (Seq 0) is always applied.
//
// Reduce is strict about the lifecycle: an event that does not fit the stream
// (a delta for a part that is not open, a second run.start, anything after
// the terminal event, a text delta on a tool_call part) returns an error
// wrapping ErrInvalidEvent, with the offending event's index. Once a gap event
// has been applied the message is Incomplete and the rule relaxes for the one
// thing a gap explains: a delta, part.end, message.end or step.finish whose
// opening event fell in the gap is skipped instead of rejected. Verbs Reduce
// does not know are ignored, since the schema is additive.
func Reduce(events []Event, prior *Message) (*Message, error) {
	m := prior.Clone()
	if m == nil {
		m = &Message{Status: StatusStreaming}
	}
	if m.Status == "" {
		m.Status = StatusStreaming
	}
	for i, ev := range events {
		if ev.Seq != 0 && ev.Seq <= m.LastSeq {
			continue
		}
		if err := m.apply(ev); err != nil {
			return nil, fmt.Errorf("%w: event %d (seq %d, %s): %w", ErrInvalidEvent, i, ev.Seq, ev.Verb, err)
		}
		if ev.Seq > m.LastSeq {
			m.LastSeq = ev.Seq
		}
	}
	return m, nil
}

func (m *Message) openPart(id string) (*Part, bool) {
	for i := len(m.Parts) - 1; i >= 0; i-- {
		if m.Parts[i].ID == id && m.Parts[i].Open {
			return &m.Parts[i], true
		}
	}
	return nil, false
}

func (m *Message) apply(ev Event) error {
	if m.Status.Done() {
		return ErrAfterTerminal
	}
	if ev.RunID != "" {
		switch {
		case m.RunID == "":
			m.RunID = ev.RunID
		case m.RunID != ev.RunID:
			return fmt.Errorf("run id %q, expected %q", ev.RunID, m.RunID)
		}
	}
	switch ev.Verb {
	case VerbRunStart:
		if m.Started {
			return fmt.Errorf("run.start twice")
		}
		m.Provider, m.Model = ev.Provider, ev.Model
		m.Started = true
	case VerbRunFinish:
		m.Status = StatusFinished
		m.FinishReason, m.RawReason = ev.Finish(), ev.RawReason
		if ev.Usage != nil {
			m.applyUsage(*ev.Usage, UsageFinal)
		}
	case VerbRunError:
		m.Status = StatusErrored
		m.Error = &RunError{Code: ev.Code, Retryable: ev.Retryable, Message: ev.Message}
	case VerbRunAbort:
		m.Status = StatusAborted
		m.AbortReason = ev.Reason
	case VerbStepStart:
		m.Steps = append(m.Steps, Step{ID: ev.StepID, Name: ev.Name, Open: true})
	case VerbStepFinish:
		for i := len(m.Steps) - 1; i >= 0; i-- {
			if m.Steps[i].ID == ev.StepID && m.Steps[i].Open {
				m.Steps[i].Open = false
				return nil
			}
		}
		if m.Incomplete {
			return nil // its opening fell in a gap
		}
		return fmt.Errorf("step.finish for step %q that is not open", ev.StepID)
	case VerbMessageStart:
		if m.MessageOpen {
			return fmt.Errorf("message.start inside message %q", m.CurrentMessage)
		}
		m.MessageOpen, m.CurrentMessage = true, ev.MessageID
		if m.ID == "" {
			m.ID, m.Role = ev.MessageID, ev.Role
		}
	case VerbMessageEnd:
		if !m.MessageOpen {
			if m.Incomplete {
				return nil // its opening fell in a gap
			}
			return fmt.Errorf("message.end with no open message")
		}
		m.MessageOpen = false
	case VerbPartStart:
		if _, dup := m.openPart(ev.PartID); dup {
			return fmt.Errorf("part %q is already open", ev.PartID)
		}
		m.Parts = append(m.Parts, Part{ID: ev.PartID, MessageID: m.CurrentMessage, Kind: ev.PartKind(), Meta: cloneRawMap(ev.Meta), Open: true})
	case VerbPartDelta:
		p, ok := m.openPart(ev.PartID)
		if !ok {
			if m.Incomplete {
				return nil // its start fell in a gap: there is nothing to attach the delta to
			}
			return fmt.Errorf("part.delta for part %q that is not open", ev.PartID)
		}
		switch {
		case ev.JSONFragment != "" && ev.Text != "":
			return fmt.Errorf("part.delta carries both text and json_fragment")
		case ev.JSONFragment != "":
			if p.Kind != PartToolCall {
				return fmt.Errorf("json_fragment on a %s part", p.Kind)
			}
			p.Args += ev.JSONFragment
		case ev.Text != "":
			if p.Kind == PartToolCall {
				return fmt.Errorf("text on a tool_call part")
			}
			p.Text += ev.Text
		}
	case VerbPartEnd:
		p, ok := m.openPart(ev.PartID)
		if !ok {
			if m.Incomplete {
				return nil // its start fell in a gap
			}
			return fmt.Errorf("part.end for part %q that is not open", ev.PartID)
		}
		p.Open = false
		if len(ev.Final) > 0 {
			p.Final = append(json.RawMessage(nil), ev.Final...)
		}
	case VerbApprovalRequest:
		m.Approvals = append(m.Approvals, Approval{ID: ev.ApprovalID, CallID: ev.CallID, Reason: ev.Reason,
			Descriptor: append(json.RawMessage(nil), ev.Descriptor...), Mode: ev.Mode, ExpiresAt: ev.ExpiresAt})
	case VerbUsage:
		if ev.Usage == nil {
			return fmt.Errorf("usage event without usage")
		}
		m.applyUsage(*ev.Usage, ev.Usage.Scope)
	case VerbActivity:
		m.Activities = append(m.Activities, Activity{Kind: ev.Kind, Value: append(json.RawMessage(nil), ev.Value...), Patch: append(json.RawMessage(nil), ev.Patch...)})
	case VerbRaw:
		if ev.Raw != nil {
			m.Raws = append(m.Raws, *ev.Raw)
		}
	case VerbGap:
		m.Gaps = append(m.Gaps, Gap{From: ev.From, To: ev.To, Reason: ev.Reason})
		m.Incomplete = true
	}
	return nil
}

func (m *Message) applyUsage(u Usage, scope UsageScope) {
	u.Extra = maps.Clone(u.Extra)
	switch scope {
	case UsageDelta:
		if m.Usage == nil {
			u.Scope = UsageCumulative
			m.Usage = &u
			return
		}
		sum := m.Usage.Add(u)
		sum.Scope = UsageCumulative
		m.Usage = &sum
	default: // cumulative and final replace what was there
		m.Usage = &u
	}
}
