package eventstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	mesh "github.com/hollis-labs/libs/mesh/core"
)

// PayloadReport is what a [PayloadSanitizer] found. It never contains values.
type PayloadReport struct {
	// Redactions is the number of values removed or masked.
	Redactions uint32
	// Paths are JSON paths of the removed values, for audit. Never values.
	Paths []string
	// Sensitivity is public, private or secret; empty means public.
	Sensitivity string
	// PII is true when personal data was detected.
	PII bool
	// ErrorClass, when set, means the payload could not be sanitized (oversized,
	// invalid JSON, opaque value) and the returned bytes must not be used.
	ErrorClass string
}

// PayloadSanitizer is the payload API of the shared redaction engine (D5).
// kind is the event kind, which selects the per-kind policy. It returns the
// sanitized bytes and a report. An error, or a report with ErrorClass set,
// makes the adapters fail closed. A nil payload is never passed in.
type PayloadSanitizer interface {
	SanitizePayload(ctx context.Context, kind string, payload []byte) ([]byte, PayloadReport, error)
}

// ClassedError lets a sanitizer name the failure class without leaking a value.
type ClassedError interface {
	error
	SanitizeErrorClass() string
}

// Mode selects how a Sanitize adapter treats an event that is already marked
// sanitized.
type Mode int

const (
	// ModeProducer sanitizes every event. It is the producer's choke point.
	ModeProducer Mode = iota
	// ModeVerifyAndScrub is the hub backstop. It re-applies the rules to an
	// event a producer already sanitized and adds to class.redactions. When a
	// producer-sanitized event still needed scrubbing the outcome Note is
	// [NoteBackstopScrubbed], so the hub can raise alert.raised.
	ModeVerifyAndScrub
)

// Outcome notes set by the Sanitize adapters.
const (
	NoteSanitized         = "sanitized"
	NoteBackstopScrubbed  = "backstop_scrubbed"
	NoteFailedClosed      = "failed_closed"
	NoteNoPayload         = "no_payload"
	errorClassUnspecified = "error"
	maxErrorClass         = 32
)

// ErrorPayload is the payload an event carries after a fail-closed sanitize.
func ErrorPayload(class string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"sanitize_error": cleanClass(class)})
	return b
}

func cleanClass(c string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(c) {
		if b.Len() == maxErrorClass {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return errorClassUnspecified
	}
	return b.String()
}

func classOf(err error) string {
	var ce ClassedError
	switch {
	case errors.As(err, &ce):
		return ce.SanitizeErrorClass()
	case errors.Is(err, ErrPanic):
		return "panic"
	}
	return errorClassUnspecified
}

var sensitivityRank = map[string]int{"": 0, "public": 0, "private": 1, "secret": 2}

// moreRestrictive returns the more restrictive of two sensitivity classes. An
// unknown class ranks above secret so it is never downgraded.
func moreRestrictive(a, b string) string {
	ra, oka := sensitivityRank[a]
	rb, okb := sensitivityRank[b]
	if !oka {
		ra = 3
	}
	if !okb {
		rb = 3
	}
	if rb > ra {
		return b
	}
	return a
}

func emptyPayload(p []byte) bool {
	t := bytes.TrimSpace(p)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// classFields is the part of an envelope's class block the sanitize stage sets.
type classFields struct {
	sanitized   bool
	redactions  uint32
	sensitivity string
	pii         bool
}

// core is the envelope-independent sanitize logic.
type sanitizeCore struct {
	name string
	s    PayloadSanitizer
	mode Mode
}

// apply sanitizes payload and returns the new payload, the new class fields
// and the outcome. cur is the event's current class state.
func (c sanitizeCore) apply(ctx context.Context, kind string, payload []byte, cur classFields) ([]byte, classFields, Outcome, error) {
	if emptyPayload(payload) {
		cur.sanitized = true
		return payload, cur, Outcome{Changed: true, Note: NoteNoPayload}, nil
	}
	out, rep, err := c.s.SanitizePayload(ctx, kind, payload)
	if err == nil && rep.ErrorClass != "" {
		err = classedError(rep.ErrorClass)
	}
	if err != nil {
		return payload, cur, Outcome{}, err
	}
	rescrub := c.mode == ModeVerifyAndScrub && cur.sanitized && rep.Redactions > 0
	cur.sanitized = true
	cur.redactions += rep.Redactions
	cur.sensitivity = moreRestrictive(cur.sensitivity, rep.Sensitivity)
	cur.pii = cur.pii || rep.PII
	note := NoteSanitized
	if rescrub {
		note = NoteBackstopScrubbed
	}
	return out, cur, Outcome{Changed: true, Note: note}, nil
}

type classedError string

func (e classedError) Error() string              { return "sanitize failed: " + string(e) }
func (e classedError) SanitizeErrorClass() string { return string(e) }

// RuntimeEventsSanitize returns the Sanitize stage for runtimeevents.Event.
func RuntimeEventsSanitize(s PayloadSanitizer, mode Mode) FailClosedStage[runtimeevents.Event] {
	return rtSanitize{sanitizeCore{name: "sanitize/runtimeevents", s: s, mode: mode}}
}

type rtSanitize struct{ sanitizeCore }

func (r rtSanitize) Name() string { return r.name }

func (r rtSanitize) Apply(ctx context.Context, e *runtimeevents.Event) (Outcome, error) {
	var cur classFields
	if e.Class != nil {
		cur = classFields{e.Class.Sanitized, e.Class.Redactions, e.Class.Sensitivity, e.Class.PII}
	}
	out, next, oc, err := r.apply(ctx, string(e.Kind), e.Payload, cur)
	if err != nil {
		return Outcome{}, err
	}
	if e.Class == nil {
		e.Class = &runtimeevents.Class{}
	}
	e.Payload = out
	e.Class.Sanitized, e.Class.Redactions, e.Class.Sensitivity, e.Class.PII = next.sanitized, next.redactions, next.sensitivity, next.pii
	return oc, nil
}

// FailClosed drops the payload, marks the event unsanitized and keeps the
// envelope so it is still emitted.
func (r rtSanitize) FailClosed(e *runtimeevents.Event, cause error) {
	e.Payload = ErrorPayload(classOf(cause))
	e.Truncated = false
	if e.Class == nil {
		e.Class = &runtimeevents.Class{}
	}
	e.Class.Sanitized = false
}

// MeshSanitize returns the Sanitize stage for mesh.Event. With
// [ModeVerifyAndScrub] it is the hub backstop.
func MeshSanitize(s PayloadSanitizer, mode Mode) FailClosedStage[mesh.Event] {
	return meshSanitize{sanitizeCore{name: "sanitize/mesh", s: s, mode: mode}}
}

type meshSanitize struct{ sanitizeCore }

func (m meshSanitize) Name() string { return m.name }

func (m meshSanitize) Apply(ctx context.Context, e *mesh.Event) (Outcome, error) {
	var cur classFields
	if e.Class != nil {
		cur = classFields{e.Class.Sanitized, e.Class.Redactions, e.Class.Sensitivity, e.Class.PII}
	}
	out, next, oc, err := m.apply(ctx, e.Kind, e.Payload, cur)
	if err != nil {
		return Outcome{}, err
	}
	if e.Class == nil {
		e.Class = &mesh.EventClass{}
	}
	e.Payload = out
	e.Class.Sanitized, e.Class.Redactions, e.Class.Sensitivity, e.Class.PII = next.sanitized, next.redactions, next.sensitivity, next.pii
	return oc, nil
}

func (m meshSanitize) FailClosed(e *mesh.Event, cause error) {
	e.Payload = ErrorPayload(classOf(cause))
	e.Truncated = false
	if e.Class == nil {
		e.Class = &mesh.EventClass{}
	}
	e.Class.Sanitized = false
}
