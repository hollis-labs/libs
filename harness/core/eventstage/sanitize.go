package eventstage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/hollis-labs/libs/security/redact"

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

// ClassedError lets a stage (Stamp, Sanitize or Classify) name its failure class
// without leaking a value; the class is all [Report.Err] keeps. Only members
// of [FailClosedClasses] are retained; any other label becomes "error".
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
	NoteSanitized        = "sanitized"
	NoteBackstopScrubbed = "backstop_scrubbed"
	// NoteBackstopUnsanitized marks an event that reached the hub backstop
	// without ever being sanitized (a producer skipping its choke point), as
	// opposed to one that failed closed.
	NoteBackstopUnsanitized = "backstop_unsanitized"
	// NoteBackstopFailedClosed marks an event that arrived at the hub already
	// failed closed. The backstop leaves it exactly as it is: the
	// {"sanitize_error":...} payload and class.sanitized=false stay, so the
	// hub can still tell it from a sanitized event.
	NoteBackstopFailedClosed = "backstop_failed_closed"
	NoteFailedClosed         = "failed_closed"
	NoteNoPayload            = "no_payload"
	// NoteTombstoned marks an event whose payload a retention pass removed
	// (payload_digest is set). There is nothing to sanitize and nothing was
	// sanitized: the adapters touch neither the payload nor class.sanitized, so
	// a tombstone is never flipped to "sanitized" and never mistaken, by the hub
	// backstop, for a producer that skipped its choke point.
	NoteTombstoned        = "tombstoned"
	errorClassUnspecified = "error"
	maxErrorClass         = 32
)

// ErrorPayload is the payload an event carries after a fail-closed sanitize.
func ErrorPayload(class string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"sanitize_error": cleanClass(class)})
	return b
}

// FailClosedClasses returns the closed failure vocabulary shared by the writer
// and hub reader. Each call returns a fresh slice, including all redact classes
// and eventstage's own failures; success is not a failure class.
func FailClosedClasses() []string {
	classes := make([]string, 0, len(redact.ErrorClasses())+3)
	for _, class := range redact.ErrorClasses() {
		classes = append(classes, string(class))
	}
	return append(classes, "panic", errorClassUnspecified, ErrorClassTombstoneWithPayload)
}

var failClosedClasses = func() map[string]struct{} {
	classes := make(map[string]struct{})
	for _, class := range FailClosedClasses() {
		classes[class] = struct{}{}
	}
	return classes
}()

func cleanClass(c string) string {
	if _, ok := failClosedClasses[c]; ok {
		return c
	}
	return errorClassUnspecified
}

func classOf(err error) string {
	var ce ClassedError
	switch {
	case errors.As(err, &ce):
		return cleanClass(ce.SanitizeErrorClass())
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

// failClosedPayload trusts only one sanitize_error member holding an exact
// closed-set class. Token decoding rejects duplicate keys rather than allowing
// a later known class to hide arbitrary data in an earlier member.
func failClosedPayload(p []byte) bool {
	if !utf8.Valid(p) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(p))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	token, err = decoder.Token()
	if err != nil || token != "sanitize_error" {
		return false
	}
	var class string
	if decoder.Decode(&class) != nil {
		return false
	}
	if _, ok := failClosedClasses[class]; !ok {
		return false
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

// ErrorClassTombstoneWithPayload is the failure class of an event that claims to
// be a retention tombstone (payload_digest set) yet still carries a payload. The
// two fields are the producer's to set, so a digest must not be a way past the
// sanitizer: such an event fails closed like any other that cannot be made safe.
const ErrorClassTombstoneWithPayload = "tombstone_with_payload"

// tombstoneOutcome decides what to do with an event whose payload_digest is set.
// A genuine tombstone has no payload (empty or JSON null) and is left alone. One
// that still has a payload is refused so the chain fails it closed; the payload
// is never inspected, trusted or passed on.
func tombstoneOutcome(payload []byte) (Outcome, error) {
	if !emptyPayload(payload) {
		return Outcome{}, classedError(ErrorClassTombstoneWithPayload)
	}
	return Outcome{Note: NoteTombstoned}, nil
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
	if c.mode == ModeVerifyAndScrub && !cur.sanitized && failClosedPayload(payload) {
		return payload, cur, Outcome{Note: NoteBackstopFailedClosed}, nil
	}
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
	skipped := c.mode == ModeVerifyAndScrub && !cur.sanitized && !failClosedPayload(payload)
	cur.sanitized = true
	cur.redactions += rep.Redactions
	cur.sensitivity = moreRestrictive(cur.sensitivity, rep.Sensitivity)
	cur.pii = cur.pii || rep.PII
	note := NoteSanitized
	switch {
	case rescrub:
		note = NoteBackstopScrubbed
	case skipped:
		note = NoteBackstopUnsanitized
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
	if e.Tombstoned() {
		return tombstoneOutcome(e.Payload)
	}
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
	e.PayloadDigest = "" // the payload is no longer removed: it is the error payload
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
	if e.Tombstoned() {
		return tombstoneOutcome(e.Payload)
	}
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
	e.PayloadDigest = "" // the payload is no longer removed: it is the error payload
	e.Truncated = false
	if e.Class == nil {
		e.Class = &mesh.EventClass{}
	}
	e.Class.Sanitized = false
}
