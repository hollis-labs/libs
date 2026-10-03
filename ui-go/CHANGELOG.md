# Changelog

All notable changes to the `ui-go` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`ui-go/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.1.0 — 2026-10-03

First release of the ui-go module: the packages of six former Hollis Labs modules, moved in with their git history.

### Added

- `ui-go/ssekit`: A Server-Sent Events writer, serve loop, resumable client and test harness that never look inside the payload (from `go-ssekit`, 10 commits of history).
- `ui-go/streamhub`: A payload-opaque, stdlib-only replayable fan-out hub with per-stream cursors, gap signals, a replay ring and slow-consumer policies (from `go-streamhub`, 12 commits of history).
- `ui-go/chatstream`: One canonical chat-stream event vocabulary with per-dialect decoders, per-target encoders, a reducer and a conformance kit (from `go-chatstream`, 11 commits of history).
- `ui-go/webui`: A small, dependency-free harness for serving a compiled single-page application out of any fs.FS, with the routing rules a SPA needs (from `go-webui`, 8 commits of history).
- `ui-go/directives`: A pure-Go parser for chat directives, the inline :: commands embedded in conversation text (from `go-directives`, 10 commits of history).
- `ui-go/envelopes`: The shared Go primitives for the Envelope UI Protocol, a wire format for typed, host-rendered payloads, with code generation and conformance tests (from `go-envelopes`, 42 commits of history).

Each lib keeps its own `CHANGELOG.md` (the history of the old module, as written) and has a `MIGRATION.md` with the old and new import paths.

### Changed

- The old modules' release tags were not carried over; this is the first release of the module.
- Import paths in code, documentation and tests moved to `github.com/hollis-labs/libs/ui-go/...`, mechanically; no symbol was renamed or changed.
- go-chatstream required go-ssekit and go-streamhub as separate modules; in this module those are ordinary imports of `ui-go/ssekit` and `ui-go/streamhub`.
