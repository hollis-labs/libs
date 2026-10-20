package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	eventSchemaID       = "urn:hollis-labs:runtimeevents:event/v1"
	ingestEventSchemaID = "urn:hollis-labs:runtimeevents:ingest-event/v1"
)

var (
	compileOnce  sync.Once
	eventChecker *jsonschema.Schema
	compileErr   error
)

// eventSchema compiles the ingest event schema once.
func eventSchema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		c := jsonschema.NewCompiler()
		c.DefaultDraft(jsonschema.Draft2020)
		c.AssertFormat()
		for _, r := range []struct {
			id  string
			doc []byte
		}{{eventSchemaID, runtimeevents.JSONSchema}, {ingestEventSchemaID, EventSchema}} {
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(r.doc))
			if err != nil {
				compileErr = err
				return
			}
			if err := c.AddResource(r.id, doc); err != nil {
				compileErr = err
				return
			}
		}
		eventChecker, compileErr = c.Compile(ingestEventSchemaID)
	})
	return eventChecker, compileErr
}

// schemaProblem describes the first schema violation without echoing values.
func schemaProblem(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		out := ve.BasicOutput()
		for _, u := range out.Errors {
			if u.InstanceLocation != "" {
				return "event does not match the ingest schema at " + u.InstanceLocation
			}
		}
	}
	return "event does not match the ingest schema"
}

// Limits. The hub may be configured with smaller values, never larger.
const (
	// MaxBatchEvents is the most events in one request.
	MaxBatchEvents = 100
	// MaxRequestBytes is the largest request body.
	MaxRequestBytes = 1 << 20
	// MaxPayloadBytes is the largest Event.Payload.
	MaxPayloadBytes = 64 << 10
)

// Status is the outcome of one event.
type Status string

const (
	// StatusAccepted means the event was stored and now has a cursor.
	StatusAccepted Status = "accepted"
	// StatusDuplicate means the id was already stored with the same payload.
	// It carries the original cursor and is not an error.
	StatusDuplicate Status = "duplicate"
	// StatusRejected means the event was not stored. The producer must not
	// retry it unchanged.
	StatusRejected Status = "rejected"
)

// Code explains a rejection.
type Code string

const (
	// CodeUnknownKind: the kind is not in the catalog.
	CodeUnknownKind Code = "unknown_kind"
	// CodeSchemaInvalid: the event is not a valid envelope, or uses a value
	// ingest does not accept (visibility "local").
	CodeSchemaInvalid Code = "schema_invalid"
	// CodeForbiddenKind: the kind is in the catalog but the producer's app may
	// not publish it.
	CodeForbiddenKind Code = "forbidden_kind"
	// CodePayloadTooLarge: the payload is over [MaxPayloadBytes].
	CodePayloadTooLarge Code = "payload_too_large"
	// CodeIDConflict: the id is already stored with a different payload.
	CodeIDConflict Code = "id_conflict"
)

// Result is the outcome for one event. Results come back in request order.
type Result struct {
	// ID is the event id as sent, empty when the event had none.
	ID     string `json:"id"`
	Status Status `json:"status"`
	// Code is set when Status is rejected.
	Code Code `json:"code,omitempty"`
	// Message is a short explanation for a person. It never contains payload
	// values.
	Message string `json:"message,omitempty"`
	// Cursor is the opaque log cursor, set when Status is accepted or
	// duplicate. It is the same string runtimeevents.Event.Cursor carries.
	Cursor string `json:"cursor,omitempty"`
}

// Response is the body of a 200 answer.
type Response struct {
	Results   []Result `json:"results"`
	Accepted  int      `json:"accepted"`
	Duplicate int      `json:"duplicate"`
	Rejected  int      `json:"rejected"`
}

// NewResponse builds a Response and its counts from results.
func NewResponse(results []Result) Response {
	r := Response{Results: results}
	if r.Results == nil {
		r.Results = []Result{}
	}
	for _, x := range results {
		switch x.Status {
		case StatusAccepted:
			r.Accepted++
		case StatusDuplicate:
			r.Duplicate++
		case StatusRejected:
			r.Rejected++
		}
	}
	return r
}

// Whole-request errors. The hub maps ErrMalformed and ErrEmpty to 400 and the
// two size errors to 413.
var (
	ErrMalformed       = errors.New("ingest: request body is not a JSON event or {\"events\":[...]}")
	ErrEmpty           = errors.New("ingest: request carries no events")
	ErrBatchTooLarge   = fmt.Errorf("ingest: more than %d events in one request", MaxBatchEvents)
	ErrRequestTooLarge = fmt.Errorf("ingest: request body over %d bytes", MaxRequestBytes)
)

// ParseRequest splits a request body into its events, one raw JSON object
// each. A body that is a single Event yields one item. Events are not
// validated here; use [CheckItem].
func ParseRequest(body []byte) ([]json.RawMessage, error) {
	if len(body) > MaxRequestBytes {
		return nil, ErrRequestTooLarge
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, ErrMalformed
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, ErrMalformed
	}
	raw, batch := probe["events"]
	if !batch {
		return []json.RawMessage{json.RawMessage(body)}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, ErrMalformed
	}
	switch {
	case len(items) == 0:
		return nil, ErrEmpty
	case len(items) > MaxBatchEvents:
		return nil, ErrBatchTooLarge
	}
	for _, it := range items {
		if t := bytes.TrimSpace(it); len(t) == 0 || t[0] != '{' {
			return nil, ErrMalformed
		}
	}
	return items, nil
}

// Rejection is why [CheckItem] refused an event.
type Rejection struct {
	Code    Code
	Message string
}

func (r *Rejection) Error() string { return string(r.Code) + ": " + r.Message }

// Result turns the rejection into the Result the hub returns for id.
func (r *Rejection) Result(id string) Result {
	return Result{ID: id, Status: StatusRejected, Code: r.Code, Message: r.Message}
}

// Options tune [CheckItem].
type Options struct {
	// AllowUnknownKinds accepts a kind that is not in the catalog. The hub
	// sets it in shadow mode only.
	AllowUnknownKinds bool
}

// CheckEnvelope applies the part of [CheckItem] that is about the shape of the
// envelope, without the catalog and without deciding who may publish what: JSON
// decodes as an Event; payload size; the event matches [EventSchema] (the generic
// envelope plus a canonical id and a required app); [runtimeevents.Event.Validate];
// visibility is not "local". A producer can run it before sending to learn what
// the hub would reject on shape, whatever the hub's allowlist says.
func CheckEnvelope(raw json.RawMessage) (runtimeevents.Event, *Rejection) {
	var ev runtimeevents.Event
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, &Rejection{CodeSchemaInvalid, "event is not a valid JSON envelope"}
	}
	if len(ev.Payload) > MaxPayloadBytes {
		return ev, &Rejection{CodePayloadTooLarge, fmt.Sprintf("payload is %d bytes, limit %d", len(ev.Payload), MaxPayloadBytes)}
	}
	schema, err := eventSchema()
	if err != nil {
		return ev, &Rejection{CodeSchemaInvalid, "ingest schema unavailable"}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return ev, &Rejection{CodeSchemaInvalid, "event is not a valid JSON envelope"}
	}
	if err := schema.Validate(inst); err != nil {
		return ev, &Rejection{CodeSchemaInvalid, schemaProblem(err)}
	}
	if err := ev.Validate(); err != nil {
		return ev, &Rejection{CodeSchemaInvalid, err.Error()}
	}
	if ev.Class != nil && ev.Class.Visibility == "local" {
		return ev, &Rejection{CodeSchemaInvalid, `class.visibility "local" is not accepted at ingest`}
	}
	return ev, nil
}

// CheckItem applies the per-event rules for a producer registered as app, in
// this order: [CheckEnvelope] (JSON decodes as an Event; payload size; the event
// matches [EventSchema], the generic envelope plus a canonical id and a required
// app; [runtimeevents.Event.Validate]; visibility is not "local"); the kind is in
// the catalog; the app may publish the kind. The payload is only required to be
// JSON: per-kind payload schemas are not validated here. It returns the decoded
// event, or a Rejection. It does not check that Event.App equals app: that is a
// whole-request 403 decided by the hub from the credential.
//
// With Options.AllowUnknownKinds an unknown kind skips the last two checks,
// because there is no catalog entry to assign an owner.
func CheckItem(app string, raw json.RawMessage, opt Options) (runtimeevents.Event, *Rejection) {
	ev, rej := CheckEnvelope(raw)
	if rej != nil {
		return ev, rej
	}
	if _, ok := catalog.Lookup(string(ev.Kind)); !ok {
		if opt.AllowUnknownKinds {
			return ev, nil
		}
		return ev, &Rejection{CodeUnknownKind, fmt.Sprintf("kind %q is not in the catalog", ev.Kind)}
	}
	if !catalog.ProducerAllows(app, string(ev.Kind)) {
		return ev, &Rejection{CodeForbiddenKind, fmt.Sprintf("app %q may not publish kind %q", app, ev.Kind)}
	}
	return ev, nil
}
