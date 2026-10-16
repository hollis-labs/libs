package runtimeevents

import (
	"errors"
	"fmt"
	"strings"

	mesh "github.com/hollis-labs/libs/mesh/core"
)

// Scope is the dimension block: low-cardinality identifiers (project,
// workstream, task, run, launch, agent, environment ...) stamped from
// verified context. A value that cannot be verified is omitted, never
// guessed. It is the same type as mesh.EventScope. scope.md in this
// directory lists each field's source of truth and the producer gaps.
type Scope = mesh.EventScope

// Corr is the correlation block.
//
// [Event.ParentID] and CausationID mean different things and may both be
// set. ParentID is structural: it links an event to another event from the
// same producer that it answers or derives from (a response to its request,
// a tool result to its tool call). CausationID is causal: it names the
// event, from any producer, that made this one happen. Every reaction
// effect sets CausationID to its trigger and Depth to the trigger's Depth
// plus one.
type Corr struct {
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
	Traceparent    string `json:"traceparent,omitempty"`
	CausationID    string `json:"causation_id,omitempty"`
	CorrelationID  string `json:"correlation_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	InReplyTo      string `json:"in_reply_to,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Depth          uint32 `json:"depth,omitempty"`
}

// Actor identifies who caused the event.
//
// URN and Kind use the mesh vocabulary: Kind is agent, user, service or
// tool and matches the URN's authority (msg://agent/..., msg://user/...).
// Role refines it: operator, system, workflow, hook or reaction. Verified
// records how the actor was identified: token, claimed or none.
type Actor struct {
	URN        string   `json:"urn,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Role       string   `json:"role,omitempty"`
	OnBehalfOf []string `json:"on_behalf_of,omitempty"`
	Verified   string   `json:"verified,omitempty"`
}

// Class is the classification block, written by the sanitize and classify
// stages.
//
// Category is one of activity, state, progress, data, message, tool,
// approval, audit, alert, fleet or step. Severity is one of debug, info,
// notice, warning, error or critical. Sensitivity is one of public,
// private or secret, and PII marks detected personal data. Visibility is
// one of local, environment, mesh or operator. PayloadSchema names the
// payload's schema as id@version. Consumers must tolerate values outside
// these lists.
type Class struct {
	Category      string   `json:"category,omitempty"`
	Severity      string   `json:"severity,omitempty"`
	Sensitivity   string   `json:"sensitivity,omitempty"`
	PII           bool     `json:"pii,omitempty"`
	Visibility    string   `json:"visibility,omitempty"`
	PayloadSchema string   `json:"payload_schema,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	Sanitized     bool     `json:"sanitized,omitempty"`
	Redactions    uint32   `json:"redactions,omitempty"`
}

// sessionKindPrefixes are the kind families that always belong to a
// session. An event of one of these kinds must carry SessionID.
var sessionKindPrefixes = []string{
	"process.", "session.", "turn.", "stdin.", "stdout.", "stderr.",
	"agent.", "policy.", "plant.", "sandbox.", "interrupt.",
}

// SessionBound reports whether events of kind k must carry a SessionID.
func SessionBound(k EventKind) bool {
	for _, p := range sessionKindPrefixes {
		if strings.HasPrefix(string(k), p) {
			return true
		}
	}
	return false
}

// ErrInvalid is matched by every error [Event.Validate] returns.
var ErrInvalid = errors.New("runtimeevents: invalid event")

// Validate checks the envelope's required fields. It does not inspect
// Payload, and it accepts unknown kinds, channels and block values.
//
// Required: SchemaVersion, ID, Kind, Time, Sequence and Source.Channel;
// at least one of SessionID or Subject; SessionID for every
// [SessionBound] kind. When Actor is set its URN and Kind are required.
// A non-zero Corr.Depth requires Corr.CausationID.
func (e Event) Validate() error {
	var missing []string
	if e.SchemaVersion == "" {
		missing = append(missing, "schema_version")
	}
	if e.ID == "" {
		missing = append(missing, "id")
	}
	if e.Kind == "" {
		missing = append(missing, "kind")
	}
	if e.Time.IsZero() {
		missing = append(missing, "time")
	}
	if e.Sequence == 0 {
		missing = append(missing, "sequence")
	}
	if e.Source.Channel == "" {
		missing = append(missing, "source.channel")
	}
	switch {
	case e.SessionID == "" && SessionBound(e.Kind):
		missing = append(missing, "session_id")
	case e.SessionID == "" && e.Subject == "":
		missing = append(missing, "session_id or subject")
	}
	if e.Actor != nil {
		if e.Actor.URN == "" {
			missing = append(missing, "actor.urn")
		}
		if e.Actor.Kind == "" {
			missing = append(missing, "actor.kind")
		}
	}
	if e.Corr != nil && e.Corr.Depth > 0 && e.Corr.CausationID == "" {
		missing = append(missing, "corr.causation_id (depth is set)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w %s: missing %s", ErrInvalid, e.ID, strings.Join(missing, ", "))
	}
	return nil
}
