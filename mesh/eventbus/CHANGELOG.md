# Changelog

## [Unreleased]

### Added

- Initial module: the hub client. `Client` with a bounded queue, batching to the
  hub's 100 events and 1 MiB limits, retry with backoff, 413 splitting and
  priority-aware eviction. An event is finished only on an explicit per-event
  verdict from the hub (accepted, duplicate or rejected); an unreadable or
  incomplete 200 is retried, never counted as sent.
- Optional bounded disk spool that keeps order and survives restarts. Records
  stay on disk until the hub settles them, the file is swapped atomically by
  generation, a torn tail is repaired, and disk use is bounded by compaction.
- Events without a valid verdict are retried for as long as the client runs
  (`MaxUnresolved` is an opt-in limit). Loss of a high-priority item to a full
  queue or spool is the last resort and is reported as `high_priority_dropped`
  and counted in `Stats.DroppedHigh`.
- `FromEnv` (a no-op `Noop` when `HOLLIS_EVENTS_URL` is unset) and `Noop`.
  Envelope-agnostic: items are raw JSON, so the module needs nothing from the
  harness group.
