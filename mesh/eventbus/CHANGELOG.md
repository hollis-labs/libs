# Changelog

## [Unreleased]

## v0.1.0 — 2026-10-20

### Added

- Initial module: the hub client. `Client` with a bounded queue, batching to the
  hub's 100 events and 1 MiB limits, retry with backoff, 413 splitting.
  An event is finished only on an explicit, valid per-event verdict (accepted or
  duplicate with a string cursor; rejected with a valid code and no cursor);
  anything else, an unreadable or incomplete 200 included, is retried and never
  counted as sent.
- Quarantine: events set aside undelivered (explicitly rejected, a request
  refused for good with 400, 403 app_mismatch or a single too-large event, or
  still without a valid verdict after `MaxUnresolved` tries, or kept failing
  with 401 or 403 for `RetryAuthMax`) go to a bounded
  `quarantine.jsonl` (oldest first), with `OnQuarantine`, `Stats.Quarantined`,
  a WARN log and `OnError`. They are never counted as sent. A 401 or a 403 other
  than app_mismatch waits and retries (a service often starts before its
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
