package runtimeevents

import (
	"bytes"
	"encoding/json"
	"fmt"

	mesh "github.com/hollis-labs/libs/mesh/core"
)

// Confidence values as mesh.EventSource.Confidence numbers. The mapping is
// fixed so that [ToMesh] and [FromMesh] are inverses.
const (
	meshConfidenceExact    = 1.0
	meshConfidenceDerived  = 0.5
	meshConfidenceInferred = 0.25
)

// ToMesh projects e onto mesh.Event. Every field of e has a place in the
// result, so FromMesh(ToMesh(e)) returns e. See projection.md for the field
// table.
//
// ToMesh does not fill fields e lacks: an event without Actor, Subject,
// Cursor or Generation projects to a mesh.Event that mesh's own Validate
// rejects until the hub stamps those fields.
func ToMesh(e Event) (mesh.Event, error) {
	out := mesh.Event{
		SchemaVersion:  e.SchemaVersion,
		ID:             e.ID,
		Kind:           string(e.Kind),
		Time:           e.Time,
		App:            e.App,
		SessionID:      e.SessionID,
		TurnID:         e.TurnID,
		ParentID:       e.ParentID,
		RawOffset:      e.RawOffset,
		Source:         mesh.EventSource{Channel: string(e.Source.Channel), Confidence: confidenceToMesh(e.Source.Confidence)},
		Subject:        mesh.URN(e.Subject),
		Generation:     e.Generation,
		SourceSequence: e.Sequence,
		Cursor:         e.Cursor,
		RecordedAt:     e.RecordedAt,
		ContentType:    e.ContentType,
		Truncated:      e.Truncated,
		Scope:          e.Scope,
		Payload:        e.Payload,
		PayloadDigest:  e.PayloadDigest,
	}
	if e.Process != (Process{}) {
		b, err := json.Marshal(e.Process)
		if err != nil {
			return mesh.Event{}, fmt.Errorf("runtimeevents: project process of %s: %w", e.ID, err)
		}
		out.Process = b
	}
	if a := e.Actor; a != nil {
		out.Actor = mesh.Actor{URN: mesh.URN(a.URN), Kind: mesh.ActorKind(a.Kind)}
		out.ActorRole = a.Role
		out.ActorVerified = a.Verified
		for _, u := range a.OnBehalfOf {
			out.OnBehalfOf = append(out.OnBehalfOf, mesh.URN(u))
		}
	}
	if c := e.Corr; c != nil {
		out.Trace = mesh.Trace{TraceID: c.TraceID, SpanID: c.SpanID, Traceparent: c.Traceparent}
		out.CausationID = c.CausationID
		out.CorrelationID = c.CorrelationID
		out.ThreadID = c.ThreadID
		out.InReplyTo = c.InReplyTo
		out.IdempotencyKey = c.IdempotencyKey
		out.Depth = c.Depth
	}
	if c := e.Class; c != nil {
		out.Visibility = c.Visibility
		out.PayloadSchema = c.PayloadSchema
		mc := mesh.EventClass{Category: c.Category, Severity: c.Severity, Sensitivity: c.Sensitivity, PII: c.PII, Labels: c.Labels, Sanitized: c.Sanitized, Redactions: c.Redactions}
		if !zeroMeshClass(mc) {
			out.Class = &mc
		}
	}
	return out, nil
}

// FromMesh projects m onto Event. It is the inverse of [ToMesh]: for every
// m that ToMesh can produce, ToMesh(FromMesh(m)) returns m.
//
// Two mesh values have no exact home and are documented in projection.md:
// a Confidence number outside the fixed table maps to the nearest lower
// grade, and keys of Process that runtimeevents does not define are dropped.
// A JSON null Payload becomes an absent Payload.
func FromMesh(m mesh.Event) (Event, error) {
	out := Event{
		SchemaVersion: m.SchemaVersion,
		ID:            m.ID,
		Kind:          EventKind(m.Kind),
		Time:          m.Time,
		App:           m.App,
		SessionID:     m.SessionID,
		TurnID:        m.TurnID,
		ParentID:      m.ParentID,
		RawOffset:     m.RawOffset,
		Source:        Source{Channel: SourceChannel(m.Source.Channel), Confidence: confidenceFromMesh(m.Source.Confidence)},
		Subject:       string(m.Subject),
		Generation:    m.Generation,
		Sequence:      m.SourceSequence,
		Cursor:        m.Cursor,
		RecordedAt:    m.RecordedAt,
		ContentType:   m.ContentType,
		Truncated:     m.Truncated,
		Scope:         m.Scope,
		PayloadDigest: m.PayloadDigest,
	}
	if len(m.Payload) > 0 && !bytes.Equal(bytes.TrimSpace(m.Payload), []byte("null")) {
		out.Payload = m.Payload
	}
	if len(m.Process) > 0 {
		if err := json.Unmarshal(m.Process, &out.Process); err != nil {
			return Event{}, fmt.Errorf("runtimeevents: project process of %s: %w", m.ID, err)
		}
	}
	if m.Actor != (mesh.Actor{}) || len(m.OnBehalfOf) > 0 || m.ActorRole != "" || m.ActorVerified != "" {
		a := &Actor{URN: string(m.Actor.URN), Kind: string(m.Actor.Kind), Role: m.ActorRole, Verified: m.ActorVerified}
		for _, u := range m.OnBehalfOf {
			a.OnBehalfOf = append(a.OnBehalfOf, string(u))
		}
		out.Actor = a
	}
	corr := Corr{
		TraceID: m.Trace.TraceID, SpanID: m.Trace.SpanID, Traceparent: m.Trace.Traceparent,
		CausationID: m.CausationID, CorrelationID: m.CorrelationID, ThreadID: m.ThreadID,
		InReplyTo: m.InReplyTo, IdempotencyKey: m.IdempotencyKey, Depth: m.Depth,
	}
	if corr != (Corr{}) {
		out.Corr = &corr
	}
	if m.Class != nil || m.Visibility != "" || m.PayloadSchema != "" {
		c := &Class{Visibility: m.Visibility, PayloadSchema: m.PayloadSchema}
		if mc := m.Class; mc != nil {
			c.Category, c.Severity, c.Sensitivity, c.PII = mc.Category, mc.Severity, mc.Sensitivity, mc.PII
			c.Labels, c.Sanitized, c.Redactions = mc.Labels, mc.Sanitized, mc.Redactions
		}
		out.Class = c
	}
	return out, nil
}

func confidenceToMesh(c Confidence) float64 {
	switch c {
	case ConfidenceExact:
		return meshConfidenceExact
	case ConfidenceDerived:
		return meshConfidenceDerived
	case ConfidenceInferred:
		return meshConfidenceInferred
	}
	return 0
}

func confidenceFromMesh(f float64) Confidence {
	switch {
	case f >= meshConfidenceExact:
		return ConfidenceExact
	case f >= meshConfidenceDerived:
		return ConfidenceDerived
	case f > 0:
		return ConfidenceInferred
	}
	return ""
}

func zeroMeshClass(c mesh.EventClass) bool {
	return c.Category == "" && c.Severity == "" && c.Sensitivity == "" && !c.PII && len(c.Labels) == 0 && !c.Sanitized && c.Redactions == 0
}
