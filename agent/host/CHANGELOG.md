# Changelog

## [Unreleased]

## [0.1.1] - 2026-11-20

- A model stream busy writing a long tool input (or thinking) is no longer given up as
  stalled: the gateway sends a `keepalive` event at most every 5 seconds while frames arrive
  that are not passed on as they are written. A stream with no frames still stalls.
- `Config.IdleTimeout`: how long a model stream may go without any event. Default 90 seconds
  (as before), used by runs and by compaction.

## [0.1.0] - 2026-11-16

- First version: an embedded agent host an application mounts over HTTP. It runs the model
  conversation through a gateway, keeps threads in SQLite, streams chatstream/v1 events, and
  relays tool calls to the page that declared them. The wire is in `WIRE.md`.
