package ingest

import _ "embed"

// EventSchema is the JSON Schema (draft 2020-12) for one event as accepted by
// POST /events: the generic Event schema (runtimeevents.JSONSchema, $id
// urn:hollis-labs:runtimeevents:event/v1, which a compiler must have registered)
// plus a canonical id (evt_ and 32 lowercase hex), a required app, and closed
// vocabularies for source.confidence and the class fields. CheckItem applies it
// to every event, including each item of a batch.
//
//go:embed schema/ingest.v1.event.schema.json
var EventSchema []byte

// RequestSchema is the JSON Schema (draft 2020-12) for the POST /events body:
// one Event, or {"events":[...]} with 1 to [MaxBatchEvents] objects. A single
// Event is checked against [EventSchema] (urn:hollis-labs:runtimeevents:ingest-event/v1),
// which the compiler must have registered together with the generic Event
// schema. The items of a batch are only required to be objects, because
// each is checked and answered on its own, so one bad event never fails the
// request.
//
//go:embed schema/ingest.v1.request.schema.json
var RequestSchema []byte

// ResponseSchema is the JSON Schema (draft 2020-12) for the 200 answer.
//
//go:embed schema/ingest.v1.response.schema.json
var ResponseSchema []byte
