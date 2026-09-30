# Changelog

All notable changes to go-ssekit are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- Package `ssekit` (standard library only): `Event`; `NewWriter`, `Writer.Send`,
  `Writer.Comment` with `WithHeader`, `WithoutBufferingHint`, `WithCacheControl`;
  `Source`, `SourceFunc`, `ChanSource`, `Merge`, `PollSource`; `Serve` with
  `WithHeartbeat`, `WithHeartbeatText`, `WithMaxLifetime`, `WithTerminal`,
  `WithOnSourceError`; `ResumeCursor` with `WithQueryKeys`, `WithPrecedence`
  (`HeaderFirst`, `QueryFirst`, `Newest`); `Read` and `WithMaxEventBytes`;
  `Client`, `NewClient`, `WithDefaults` and `Client.Stream` with `WithBackoff`,
  `WithIdleTimeout`, `WithStreamMaxEventBytes`, `WithIsTerminal`,
  `WithIsFinalStatus`, `WithContinuity`, `WithMaxReconnects`, `WithOnReconnect`;
  `StatusError`, `GapError` and the sentinel errors. `WithMaxServerRetry`
  (default 5 minutes, `DefaultMaxServerRetry`) caps a server-sent `retry:`.
- Documented limitation: `Serve` cannot preempt a write blocked on a stalled
  client connection (deadlines are cleared); use TCP keepalive or a per-write
  deadline via `http.ResponseController`.
- Package `ssetest`: `Script` (a scripted hostile server with `Emit`, `Drop`,
  `Close`, `Overlap`, `Gap`, `Status`, `Stall`, `Burst`, `Retry`, `Comment`,
  `Raw`, `Send`, `Wait`), `Record`, `Replay`, `Concat`.
- The parser is written in this module: the `tmaxmax/go-sse` v0.11.0 spike
  dispatched incomplete events at EOF and does not expose `retry:` (see README).
- Lifted from Nanite, Tangent and Tether SSE handlers and the `go-tether-client`
  reader, transcribed from reading those files. Byte expectations for `Send`
  come from a run of `testdata/capture`, a copy of the framing statements, not
  from a run of those applications.
