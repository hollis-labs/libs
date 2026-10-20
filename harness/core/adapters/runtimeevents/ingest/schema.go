package ingest

import _ "embed"

// RequestSchema is the JSON Schema (draft 2020-12) for the POST /events body:
// one Event, or {"events":[...]} with 1 to [MaxBatchEvents] objects. A single
// Event is checked against the Event schema (runtimeevents.JSONSchema, $id
// urn:hollis-labs:runtimeevents:event/v1), which the compiler must have
// registered. The items of a batch are only required to be objects, because
// each is checked and answered on its own, so one bad event never fails the
// request.
//
//go:embed schema/ingest.v1.request.schema.json
var RequestSchema []byte

// ResponseSchema is the JSON Schema (draft 2020-12) for the 200 answer.
//
//go:embed schema/ingest.v1.response.schema.json
var ResponseSchema []byte
