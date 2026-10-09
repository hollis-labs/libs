# Changelog

## Retirement — 2026-10-09

- Redirect new development to `github.com/hollis-labs/substrate/mesh/tetherclient` in `github.com/hollis-labs/substrate/mesh v0.1.0`.
- The successor is the Tether client. Adapt legacy mux names, socket defaults and endpoint contracts explicitly; this does not assert API equivalence or change existing consumer pins.
- Archive after the final redirect merge; retain all historical source and tags.

All notable changes to go-agentmux-client are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**This module is deprecated.** Use `github.com/hollis-labs/go-tether-client`.
It is retained only so existing consumers of this import path keep compiling.
This file was backfilled from the git history.

## [Unreleased]

### Changed

- Deprecated in favor of `go-tether-client` (notice in the README; the repository is archived as a pointer to its successor).
- Added project `AGENTS.md`.

## [0.3.0] - 2026-04-21

### Changed

- **Breaking:** bumped `go-messaging` to v0.2.0; `Subscribe` now runs over the `/messages/subscribe` SSE endpoint.

### Removed

- The dead SSE `Subscribe` draft; in-process fan-out is the correct design.

## [0.2.0] - 2026-04-21

### Added

- `messaging.Store` and `Dispatcher` implementations over the daemon's `/messages/*` endpoints.

## [0.1.0] - 2026-04-20

### Added

- Initial Agent Mux client package (`agentmux`): session, launch and event API client with typed API errors.
- `provider_id` on `LaunchResponse`.

[Unreleased]: https://github.com/hollis-labs/go-agentmux-client/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/hollis-labs/go-agentmux-client/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/hollis-labs/go-agentmux-client/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/hollis-labs/go-agentmux-client/releases/tag/v0.1.0
