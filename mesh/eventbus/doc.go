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
//   - Publish never blocks on the network and never returns an error. Callbacks
//     (OnDrop, OnReject, OnError) are never called with an internal lock held.
//   - Delivery is in order and at least once. The hub dedupes on the envelope
//     id, so a retried batch is harmless.
//   - An event is finished only when the hub gives it an explicit verdict:
//     "accepted" or "duplicate" count as sent, "rejected" goes to OnReject and
//     is never retried. A 200 that is unreadable, omits an event, or names an
//     unknown status leaves those events unresolved: they are sent again, alone,
//     with backoff, and dropped (reason "unresolved") only after
//     Config.MaxUnresolved tries. Events with a verdict are never re-sent.
//   - A request is at most 100 events and about 900 KiB. A 413 halves the
//     window; a single event the hub refuses as too large is rejected.
//   - Network errors, 401, 403 (scope_required), 404/405 (ingest not enabled on
//     the hub yet), 429 and 5xx are retried with doubling backoff (Retry-After
//     honoured): a credential may be rotated or a scope not granted yet, and the
//     events are kept meanwhile. OnError reports each failure. A 403
//     app_mismatch (the events name another app than the credential) can never
//     succeed, so that batch is dropped and counted rather than blocking
//     everything behind it. Other 4xx drop the batch and count it as rejected.
//   - When the in-memory queue is full and there is no spool, the oldest
//     [PriorityLow] item is evicted to make room. If everything held is
//     high priority, an incoming low-priority item is the one dropped, and an
//     incoming high-priority item replaces the oldest.
//
// Disk spool (Config.SpoolDir): items that do not fit in memory, and anything
// unsent at Close, are written to a bounded spool and resumed by the next Client
// on that directory. A record stays on disk until the hub has given its event a
// verdict, so a crash with a request in flight loses nothing (the event may be
// sent again). The spool is replaced atomically by generation, a torn last line
// is cut off when it is opened, and the file is compacted as records are
// settled, so the disk use stays within about 1.34 times SpoolMaxBytes. When the
// cap is reached the oldest low-priority records not yet loaded for sending go
// first, then the oldest of any priority. Appends are not synced one by one: the
// spool survives a process crash, not necessarily a power loss. Order is kept:
// once the spool holds unsettled records, new items go behind them.
//
// What it is not: a durable outbox. Items still in memory are lost if the
// process is killed (with a spool, only those that never reached it). An app that must not lose events writes them to its own
// outbox in the same transaction as the change and replays that outbox; this
// client is the transport for that replay as well as for fire-and-forget
// events.
//
// Wire format (settled with the hub): POST {"events":[<envelope>...]} with a
// bearer token; HTTP 200 carries per-item results
// {"results":[{"id","status","code","message","cursor"}]} where status is
// accepted, duplicate or rejected. A bare JSON array of the same objects is
// also accepted.
package eventbus
