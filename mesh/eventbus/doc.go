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
// Guarantees:
//
//   - Publish never blocks on the network and never returns an error.
//   - Delivery is in order and at least once. The hub dedupes on the envelope
//     id, so a retried batch is harmless; a "duplicate" result counts as sent.
//   - A request is at most 100 events and about 900 KiB. A 413 splits the
//     batch; a single event the hub refuses as too large is rejected.
//   - Network errors, 401/403, 429 and 5xx are retried with doubling backoff
//     (Retry-After honoured). 401/403 are retried because a credential may be
//     rotated or not yet issued; OnError reports them. Other 4xx drop the batch
//     and count it as rejected. A per-item "rejected" result is counted, passed
//     to OnReject and never retried.
//   - When the in-memory queue is full the oldest [PriorityLow] item is
//     evicted first. With a spool directory configured, overflow goes to disk
//     instead (bounded; oldest low-priority items are dropped first, then the
//     oldest of any priority), and anything unsent at Close stays there for
//     the next Client on that directory. Without a spool, unsent items at
//     Close are dropped and counted.
//
// What it is not: a durable outbox. Items still in memory are lost if the
// process is killed. An app that must not lose events writes them to its own
// outbox in the same transaction as the change and replays that outbox; this
// client is the transport for that replay as well as for fire-and-forget
// events.
//
// Wire format (settled with the hub): POST {"events":[<envelope>...]} with a
// bearer token; HTTP 200 carries per-item results
// {"results":[{"id","status","code","message","cursor"}]} where status is
// accepted, duplicate or rejected. A bare JSON array of the same objects is
// also accepted, and an unreadable 200 body counts every item as accepted.
package eventbus
