# Changelog

## [Unreleased]

## v0.2.0 — 2026-10-20

### Added

- `Client.Deliver(ctx, items)`: synchronous delivery for a caller that keeps its
  own durable record (an outbox relay). It bypasses the queue and spool, applies
  the same rules as the queue (strict verdicts, backoff, quarantine for what can
  never succeed) and returns a `Result` per item: a `Disposition` (`Sent`,
  `Quarantined`, or `Pending` when the context ended or the client closed), a
  `Duplicate` flag and, for a quarantined item, the `Reason` and `Code`. An item
  is `Quarantined` only once its record is durably written; if the file cannot be
  written it is retried and stays `Pending`. A 401 or 403 `scope_required` is
  waited out for as long as the context lives (the `RetryAuthMax` ceiling
  protects the queue's bounded memory, not a caller with a durable record), so a
  missing credential cannot drain an outbox into quarantine; `StrictAuth` still
  makes it final. `Close` waits for a running `Deliver` and cancels its in-flight
  request. It keeps its own retry state, so it can run next to the queue sender.
  `Deliver` requires `Config.SpoolDir`: `Quarantined` means "recorded in
  `quarantine.jsonl`", so a client without one leaves every item `Pending` and
  reports an error rather than let a relay skip an event it has no copy of. A
  closed client, or one closed between chunks, sends nothing more. The relay moves
  its cursor only over `Sent` and `Quarantined`; any other disposition stops it.
- Package `relay`: ships an app's local outbox to the hub. The app supplies a
  `Source` (entries after a sequence number, in order; optionally `Head` for lag)
  and a `Cursor` (the last settled sequence number; `FileCursor` is one). The
  relay moves the cursor only over the contiguous prefix of entries the hub
  settled or the client durably quarantined, resumes from it after a restart, and
  reports and retries failures of the source, the cursor store and the hub. At
  least once; the hub dedupes on the event id. `Stats` report sent, duplicates,
  rejected, quarantined, cursor, head and lag.

### Fixed

- An item whose body is not valid JSON can now be quarantined (it is stored as a
  JSON string of its raw bytes; reason `invalid`).

## v0.1.0 — 2026-10-20

### Added

- Initial module: the hub client. `Client` with a bounded queue, batching to the
  hub's 100 events and 1 MiB limits, retry with backoff, 413 splitting.
  An event is finished only on an explicit, valid per-event verdict (accepted or
  duplicate with a string cursor; rejected with a valid code and no cursor);
  anything else, an unreadable or incomplete 200 included, is retried and never
  counted as sent.
- Quarantine: events set aside undelivered (explicitly rejected, a request
  refused for good with 400, any other 403 or a single too-large event, or
  still without a valid verdict after `MaxUnresolved` tries, or kept failing
  with 401 or 403 scope_required for `RetryAuthMax`) go to a bounded
  `quarantine.jsonl` (oldest first), with `OnQuarantine`, `Stats.Quarantined`,
  a WARN log and `OnError`. They are never counted as sent. A 401 or a 403
  scope_required waits and retries (a service often starts before its
  credential exists), up to `RetryAuthMax` (default 10 minutes); `StrictAuth`
  makes them final at once.
  Recovery is manual in this version (see the package documentation).
- Priorities `PriorityDebug`, `PriorityActivity`, `PriorityHigh`: a full queue or
  spool drops debug first, then activity, and protected items only as the last
  resort. Losing a protected item is recorded (`Loss`: class, first and last
  time, count; persisted in `loss.json`), counted in `Stats.DroppedHigh`, logged
  at WARN, reported to `OnDrop` as `high_priority_dropped`, and announced by the
  event `Config.GapMarker` builds. The record is cleared only by the hub's
  acceptance of that marker, and only for the items it announced.
- Optional bounded disk spool that keeps order and survives restarts. Records
  stay on disk until the hub settles them, the file is swapped atomically by
  generation, a torn tail is repaired, and disk use is bounded by compaction.
- With no token, no Authorization header is sent (for a local relay that holds
  the credential).
- `FromEnv` (a no-op `Noop` when `HOLLIS_EVENTS_URL` is unset) and `Noop`.
  Envelope-agnostic: items are raw JSON, so the module needs nothing from the
  harness group.
