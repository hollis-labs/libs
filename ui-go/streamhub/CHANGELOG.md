# Changelog

All notable changes to go-streamhub are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- `Log` (the persistence seam: `Append`, `After`, `Head`, `Trim`, `Close`) and
  `MemoryLog`, a bounded in-memory ring per stream. `Log.After` returns a
  `*GapError` (matching `ErrGap`, and `ErrCursorAhead` for a cursor past the
  head) instead of a partial result.
- `Hub`: `Publish` (Append and fan-out serialized per stream), `Subscribe`
  (register pending under the lock, replay outside it, de-duplicate by Seq),
  `Close` with `WithFinalizer`, `Open`, `Head`, `Shutdown`.
- Gap notices as in-band `Item{Gap}` values, or as `*GapError` with
  `SubscribeOptions.GapAsError`. `GapRetention`, `GapCursorAhead`, `GapDropped`.
- Slow-consumer policies: `CloseAndResume` (default), `Block`, `DropNewest`,
  `DropOldest`, `EvictAfterN`. Every loss surfaces as a gap with an exact count,
  or as a `*SlowConsumerError` carrying the resume cursor.
- Terminal guard (`WithTerminal`), retention grace for ended streams
  (`WithRetainAfterClose`, default 60s), `FromLatest`, per-subscription `Filter`.
- `hubtest`: `Conformance` (Log backends), `HubSuite` (hub behavior over any
  Log), and the `GatedLog`, `Next`, `Drain` and `Seqs` helpers.
- Tests ported as scenarios from Nanite's `stream_replay_test.go` and Tether's
  `bus_test.go`/`bus_integration_test.go`; a synctest concurrency and leak
  suite; a fan-out benchmark (100 fast subscribers plus one slow).

### Known limitations

- Retention is per stream, not per event class. With one stream per session and
  the channel in `Event.Name`, a high-volume delta channel can evict low-volume
  control events (approvals, terminal) before a client replays them. Open: it
  needs a rule (retention by event class, or two logs); not solved in this
  release. See the README, "Known limitations / open questions".
