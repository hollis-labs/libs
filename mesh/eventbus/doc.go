// Package eventbus is the producer-side client for the shared event hub: it
// delivers already-built event envelopes to the hub's ingest route
// (POST /events) and keeps the calling app running when the hub is slow, down
// or not configured.
//
// The package does not know the envelope. An [Item] carries the complete JSON
// body (a runtimeevents v1.1 envelope), its id and a [Priority]; a thin adapter
// in the harness group turns a runtimeevents.Event into an Item, which is why
// this module imports nothing from harness.
//
// # Delivery
//
//   - Publish never blocks on the network and never returns an error. Callbacks
//     (OnDrop, OnReject, OnQuarantine, OnError) are never called with an
//     internal lock held.
//   - Delivery is in order and at least once. The hub dedupes on the envelope
//     id, so a retried batch is harmless.
//   - An event is finished only when the hub gives it an explicit, valid verdict
//     (see the wire format below): accepted or duplicate count as sent. An
//     explicit rejected is final: the event is quarantined and OnReject is
//     called. Events with a verdict are never re-sent.
//   - A 200 that is unreadable, omits an event, or gives an invalid verdict
//     leaves those events unresolved: they are sent again, alone, with backoff.
//     After Config.MaxUnresolved tries (default 8) they are quarantined, so one
//     bad event cannot hold up the queue behind it. They are never counted as
//     sent and never dropped as if delivered.
//   - A request is at most 100 events and about 900 KiB. A 413 halves the
//     window; a single event the hub refuses as too large is quarantined.
//   - Retried with doubling backoff (Retry-After honoured): network errors, 429,
//     5xx, and 404/405 (the hub's ingest is not enabled yet). Final for the
//     batch, which is quarantined and reported through OnError: 400, 401, 403
//     (including 403 app_mismatch, which can never succeed) and any other 4xx.
//     Config.RetryAuth makes 401 and a 403 other than app_mismatch retried
//     instead, for setups where a credential may be rotated or granted later.
//   - With no Token and no TokenFile no Authorization header is sent at all, and
//     that is not an error: a local relay that holds the credential needs none.
//
// # Quarantine
//
// Quarantined events were not delivered and will not be retried. They are
// counted (Stats.Quarantined), passed to OnQuarantine, reported through OnError
// and logged at WARN. With a spool directory they are also kept in
// quarantine.jsonl, bounded by Config.QuarantineMaxBytes with the oldest records
// going first. Without one, only the callback and the counters remain.
//
// # Overflow
//
// Memory and disk are bounded, so loss under a long outage is possible and is
// reported, not hidden. What goes first is fixed: debug items, then activity
// items, and protected items (PriorityHigh: state, approval, audit, alert) only
// as the last resort. An incoming item less important than everything held is
// refused before any protected item is evicted.
//
// Losing a protected item is recorded: a [Loss] with the class, the first and
// last time something was dropped and a count is persisted (loss.json in the
// spool directory), counted in Stats.DroppedHigh, logged at WARN and reported to
// OnDrop with the reason "high_priority_dropped". The next time the hub accepts
// events, Config.GapMarker, if set, builds an event announcing the gap, which is
// published and the record cleared. An app that cannot accept even that loss
// keeps its own outbox and replays it through a relay.
//
// # Disk spool
//
// With Config.SpoolDir, items that do not fit in memory, and anything unsent at
// Close, are written to a bounded spool and resumed by the next Client on that
// directory. A record stays on disk until the hub has given its event a verdict,
// so a crash with a request in flight loses nothing (the event may be sent
// again). The spool is replaced atomically by generation, a torn last line is
// cut off when it is opened, and the file is compacted as records settle, so the
// disk use stays within about 1.34 times SpoolMaxBytes. Appends are not synced
// one by one: the spool survives a process crash, not necessarily a power loss.
// Order is kept: once the spool holds unsettled records, new items go behind
// them.
//
// What it is not: a durable outbox. Items still in memory are lost if the
// process is killed (with a spool, only those that never reached it). An app
// that must not lose events writes them to its own outbox in the same
// transaction as the change and replays that outbox; this client is the
// transport for that replay as well as for fire-and-forget events.
//
// # Wire format
//
// Settled with the hub (libs ingest contract): POST {"events":[<envelope>...]}
// with a bearer token. HTTP 200 carries
// {"results":[{"id","status","code","message","cursor"}],"accepted":N,
// "duplicate":N,"rejected":N}, one result per event in request order, each with
// the event's id. accepted and duplicate need a cursor that is a non-empty JSON
// string and no code; rejected needs one of unknown_kind, schema_invalid,
// forbidden_kind, payload_too_large or id_conflict and no cursor. The counts are
// derived and not trusted. Anything else is no verdict and the event is
// retried: a body that is not that object (a bare array included), a result for
// another id or out of order, an unknown status, a cursor of another type, a
// missing cursor. If the result count differs from the request count, results
// are matched by id, and an id that occurs twice in one request gets no verdict.
package eventbus
