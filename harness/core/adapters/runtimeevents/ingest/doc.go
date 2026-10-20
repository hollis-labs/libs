// Package ingest is the contract for publishing events to the hub with
// POST /events (event bus ingest, Delegation 013).
//
// A producer sends [runtimeevents.Event] values, unchanged. The body is either
// one Event or {"events":[Event,...]} with at most [MaxBatchEvents] events and
// [MaxRequestBytes] bytes. The hub answers 200 with a [Response] that carries
// one [Result] per event, in order, so one bad event never fails its
// siblings. Whole-request failures use the HTTP status instead: 400 for a body
// that cannot be read, 401 and 403 for credentials and app binding, 413 for a
// request over the limits, 429 with Retry-After when rate limited, and 503
// when the hub is not ready. A producer retries 429, 503 and network errors
// with backoff, never a rejected event.
//
// # What the producer owns, what the hub owns
//
// The producer mints id (runtimeevents.NewEventID) once and reuses it on every
// retry. The id is the only dedupe key. The same id with the same payload is
// reported as duplicate with the original cursor, which is success. The same
// id with a different payload is rejected with [CodeIDConflict].
//
// The hub assigns cursor and recorded_at, and overwrites actor, the class
// fields it computes (sanitized, redactions, sensitivity, pii, visibility) and
// the scope dimensions it can verify. A producer must not send them; the hub
// ignores what it sends. A producer never sends visibility "local".
//
// # Check
//
// [ParseRequest] splits a body into events and enforces the request limits.
// [CheckItem] applies the per-event rules in a fixed order. The hub uses both,
// and a client may use them to avoid sending an event that would be rejected.
// JSON Schemas for the request and the response are [RequestSchema] and
// [ResponseSchema]; fixtures for other languages are in testdata.
package ingest
