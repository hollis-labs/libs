package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

type ErrorCode string

const (
	ErrorInvalid     ErrorCode = "invalid"
	ErrorUnsupported ErrorCode = "unsupported"
	ErrorNotFound    ErrorCode = "not_found"
	ErrorConflict    ErrorCode = "conflict"
	ErrorDenied      ErrorCode = "denied"
	ErrorLimit       ErrorCode = "limit"
)

type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string             { return string(e.Code) + ": " + e.Message }
func NewError(c ErrorCode, m string) error { return &Error{c, m} }

// Capability identifies an exact version. Negotiation never substitutes a version.
type Capability struct {
	URI   string   `json:"uri"`
	Verbs []Verb   `json:"verbs"`
	Modes []string `json:"modes,omitempty"`
}
type Descriptor struct {
	Provider      URN            `json:"provider"`
	Capabilities  []Capability   `json:"capabilities"`
	TaskStates    []TaskState    `json:"task_states"`
	SessionStates []SessionState `json:"session_states"`
	DefaultLimits Limits         `json:"default_limits"`
}

var capabilityURI = regexp.MustCompile(`^urn:[a-zA-Z0-9][a-zA-Z0-9:._-]*/v[1-9][0-9]*$`)

func (d Descriptor) Validate() error {
	if err := d.Provider.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range d.Capabilities {
		if !capabilityURI.MatchString(c.URI) || seen[c.URI] {
			return fmt.Errorf("invalid or duplicate capability URI %q", c.URI)
		}
		seen[c.URI] = true
	}
	for _, s := range d.TaskStates {
		if !s.Valid() {
			return fmt.Errorf("invalid task state %q", s)
		}
	}
	for _, s := range d.SessionStates {
		if !s.Valid() {
			return fmt.Errorf("invalid session state %q", s)
		}
	}
	if err := ValidateSpawnCapabilities(d); err != nil {
		return err
	}
	return d.DefaultLimits.Validate()
}
func (d Descriptor) Supports(v Verb) bool {
	for _, c := range d.Capabilities {
		for _, w := range c.Verbs {
			if v == w {
				return true
			}
		}
	}
	return false
}

type Requirement struct {
	URI   string   `json:"uri"`
	Verbs []Verb   `json:"verbs,omitempty"`
	Modes []string `json:"modes,omitempty"`
}

func NegotiateCapabilities(d Descriptor, requirements []Requirement) ([]Capability, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out := make([]Capability, 0, len(requirements))
	for _, r := range requirements {
		found := false
		for _, c := range d.Capabilities {
			if c.URI != r.URI {
				continue
			}
			for _, v := range r.Verbs {
				ok := false
				for _, cv := range c.Verbs {
					ok = ok || cv == v
				}
				if !ok {
					return nil, NewError(ErrorUnsupported, "required verb "+string(v))
				}
			}
			for _, mode := range r.Modes {
				ok := false
				for _, m := range c.Modes {
					ok = ok || m == mode
				}
				if !ok {
					return nil, NewError(ErrorUnsupported, "required mode "+mode)
				}
			}
			out = append(out, c)
			found = true
			break
		}
		if !found {
			return nil, NewError(ErrorUnsupported, "required capability "+r.URI)
		}
	}
	return out, nil
}

type EventSource struct {
	Channel    string  `json:"channel"`
	Confidence float64 `json:"confidence,omitempty"`
}
type Trace struct {
	TraceID     string `json:"trace_id,omitempty"`
	SpanID      string `json:"span_id,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
}

// Event keeps durable replay Cursor independent of source generation/sequence.
type Event struct {
	SchemaVersion  string          `json:"schema_version"`
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Time           time.Time       `json:"time"`
	App            string          `json:"app,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	TurnID         string          `json:"turn_id,omitempty"`
	ParentID       string          `json:"parent_id,omitempty"`
	Process        json.RawMessage `json:"process,omitempty"`
	Source         EventSource     `json:"source"`
	Actor          Actor           `json:"actor"`
	OnBehalfOf     []URN           `json:"on_behalf_of,omitempty"`
	Subject        URN             `json:"subject"`
	CausationID    string          `json:"causation_id,omitempty"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	ThreadID       string          `json:"thread_id,omitempty"`
	InReplyTo      string          `json:"in_reply_to,omitempty"`
	Generation     uint64          `json:"generation"`
	SourceSequence uint64          `json:"source_sequence"`
	Cursor         string          `json:"cursor"`
	Trace          Trace           `json:"trace,omitempty"`
	ContentType    string          `json:"content_type"`
	PayloadSchema  string          `json:"payload_schema,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Visibility     string          `json:"visibility"`
	Truncated      bool            `json:"truncated"`
	Payload        json.RawMessage `json:"payload"`

	// Envelope v1.1 fields. All are optional and additive; Validate does not
	// require them and SchemaVersion stays "1". They exist so the
	// runtimeevents envelope projects onto Event without loss.

	// RawOffset is the byte offset into the producer's raw log, when known.
	RawOffset *int64 `json:"raw_offset,omitempty"`
	// RecordedAt is set by the durable log on append, never by producers.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// Depth counts hops through reactions; a reaction effect carries its
	// trigger's Depth plus one. CausationID names the trigger.
	Depth uint32 `json:"depth,omitempty"`
	// ActorRole refines Actor.Kind: operator, system, workflow, hook or
	// reaction. Open vocabulary.
	ActorRole string `json:"actor_role,omitempty"`
	// ActorVerified records how Actor was identified: token, claimed or none.
	ActorVerified string `json:"actor_verified,omitempty"`
	// Scope holds verified dimension identifiers stamped by the producer.
	Scope *EventScope `json:"scope,omitempty"`
	// Class holds classification set by the sanitize and classify stages.
	// Visibility and PayloadSchema stay top-level fields of Event.
	Class *EventClass `json:"class,omitempty"`
}

// EventScope is the dimension block of an event: low-cardinality identifiers
// only, each stamped from verified context and omitted when unverified.
type EventScope struct {
	EnvironmentID  string `json:"environment_id,omitempty"`
	Authority      string `json:"authority,omitempty"`
	WorkerID       string `json:"worker_id,omitempty"`
	Host           string `json:"host,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	WorkstreamID   string `json:"workstream_id,omitempty"`
	EpicID         string `json:"epic_id,omitempty"`
	SprintID       string `json:"sprint_id,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	PhaseID        string `json:"phase_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Attempt        uint32 `json:"attempt,omitempty"`
	LaunchID       string `json:"launch_id,omitempty"`
	AgentURN       string `json:"agent_urn,omitempty"`
	LogicalAgentID string `json:"logical_agent_id,omitempty"`
	TeamRunID      string `json:"team_run_id,omitempty"`
}

// EventClass is the classification block of an event. Category is one of
// activity, state, progress, data, message, tool, approval, audit, alert,
// fleet or step; Severity one of debug, info, notice, warning, error or
// critical; Sensitivity one of public, private or secret. Consumers must
// tolerate values outside these lists.
type EventClass struct {
	Category    string   `json:"category,omitempty"`
	Severity    string   `json:"severity,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`
	PII         bool     `json:"pii,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Sanitized   bool     `json:"sanitized,omitempty"`
	Redactions  uint32   `json:"redactions,omitempty"`
}

func (e Event) Validate() error {
	if e.SchemaVersion == "" || e.ID == "" || e.Kind == "" || e.Time.IsZero() || e.Cursor == "" || e.Generation == 0 || e.SourceSequence == 0 || !json.Valid(e.Payload) {
		return NewError(ErrorInvalid, "incomplete event envelope")
	}
	if err := e.Actor.Validate(); err != nil {
		return err
	}
	return e.Subject.Validate()
}

// InstanceView is the provider response view of an executing agent instance.
type InstanceView struct {
	URN          URN          `json:"urn"`
	SessionURN   URN          `json:"session_urn"`
	Parent       URN          `json:"parent,omitempty"`
	SessionState SessionState `json:"session_state"`
	Limits       Limits       `json:"limits"`
}
type Task struct {
	ID      URN           `json:"id"`
	Agent   URN           `json:"agent"`
	Caller  Actor         `json:"caller"`
	Parent  URN           `json:"parent,omitempty"`
	State   TaskState     `json:"state"`
	History HistoryPolicy `json:"history"`
	Result  []byte        `json:"result,omitempty"`
}

// Member keeps functional slot and role label separate. ID is the provider's
// membership identifier used by @member-id addressing.
type Member struct {
	ID   string `json:"id"`
	Slot string `json:"slot"`
	Role string `json:"role,omitempty"`
}

type Team struct {
	URN     URN            `json:"urn"`
	Members map[URN]Member `json:"members"`
	// RosterVersion is this provider's counter, independent of a host's
	// roster-store version. Hosts retain their own resolved roster snapshot.
	RosterVersion uint64 `json:"roster_version"`
}
type Message struct {
	ID            string          `json:"id"`
	Sender        Actor           `json:"sender"`
	Recipients    []URN           `json:"recipients"`
	Body          json.RawMessage `json:"body"`
	InReplyTo     string          `json:"in_reply_to,omitempty"`
	RosterVersion uint64          `json:"roster_version,omitempty"`
	Delivery      DeliveryPolicy  `json:"delivery,omitempty"`
}

// Request is the portable command union. Target names the resource; Team scopes
// member/address verbs. Parent records launch/task lineage; InReplyTo names a message.
type Request struct {
	Admission  *AdmissionConstraints `json:"admission,omitempty"`
	Lookup     *LookupRequest        `json:"lookup,omitempty"`
	Follow     *FollowRequest        `json:"follow,omitempty"`
	Result     *VersionedResult      `json:"versioned_result,omitempty"`
	Verb       Verb                  `json:"verb"`
	Actor      Actor                 `json:"actor"`
	OnBehalfOf []URN                 `json:"on_behalf_of,omitempty"`
	Target     URN                   `json:"target,omitempty"`
	Team       URN                   `json:"team,omitempty"`
	Parent     URN                   `json:"parent,omitempty"`
	// Address is resolved by the provider using its own roster and dispatch.
	// A host that resolves and authorizes routing itself sends the retained
	// recipient URNs with MessageSend instead of resolving the address twice.
	Address        string          `json:"address,omitempty"`
	Slot           string          `json:"slot,omitempty"`
	Role           string          `json:"role,omitempty"`
	InReplyTo      string          `json:"in_reply_to,omitempty"`
	Delivery       DeliveryPolicy  `json:"delivery,omitempty"`
	Body           json.RawMessage `json:"body,omitempty"`
	History        HistoryPolicy   `json:"history,omitempty"`
	Limits         Limits          `json:"limits,omitempty"`
	Cascade        bool            `json:"cascade,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
}
type Response struct {
	Receipt  *AssignmentReceipt `json:"receipt,omitempty"`
	Snapshot *TaskSnapshot      `json:"snapshot,omitempty"`
	Replay   *ReplayPage        `json:"replay,omitempty"`
	Instance *InstanceView      `json:"instance,omitempty"`
	Task     *Task              `json:"task,omitempty"`
	Team     *Team              `json:"team,omitempty"`
	Message  *Message           `json:"message,omitempty"`
	Events   []Event            `json:"events,omitempty"`
}

// Provider implementations expose live capabilities. Unsupported commands return
// ErrorUnsupported; no provider may silently downgrade a requested operation.
type Provider interface {
	Describe(context.Context) (Descriptor, error)
	Invoke(context.Context, Request) (Response, error)
}
