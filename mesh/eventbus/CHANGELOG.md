# Changelog

## [Unreleased]

### Added

- Initial module: `Client` (bounded queue, batching to the hub's 100 events and
  1 MiB limits, per-item result handling, retry with backoff, 413 splitting,
  priority-aware eviction), an optional bounded disk spool that keeps order and
  survives restarts, `FromEnv` (no-op when `HOLLIS_EVENTS_URL` is unset) and
  `Noop`. Envelope-agnostic: items are raw JSON, so the module needs nothing
  from the harness group.
