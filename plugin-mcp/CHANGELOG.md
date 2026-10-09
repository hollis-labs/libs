# Changelog

All notable changes to the `plugin-mcp` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`plugin-mcp/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## [0.1.1] - 2026-10-09

### Fixed

- Keep host-service callback completion separate from request cancellation, so
  successful results and policy refusals are not randomly replaced by cancellation
  or unknown-outcome errors. Actual cancellation retains its existing semantics.

- Select Go 1.26.9 for CI and release builds to include standard-library
  vulnerability fixes, retaining the Go 1.26.8 language floor.

- Verify acknowledgement gate independence at the disposal callback boundary,
  rather than requiring child shutdown within 100 ms. A separate regression
  preserves the bounded state-store wait and refusal of timed-out acknowledgement.

## [0.1.0] - 2026-10-07

### Added

- History-preserving imports of plugin-sdk, plugin-host, mcp-host, go-mcp,
  go-mcp-sanitize and api-projection under one module.
- Final Go import paths under `github.com/hollis-labs/libs/plugin-mcp`,
  with each original package root retained and npm package names unchanged.
- Original dependency requirements and MIT licenses retained. Plugin hooks
  remain a separately released dependency.
- Protocol-2 inprocess MCP startup with trusted owner-supplied initialization,
  fresh per-attempt generations, verified Init agreement before Load, SDK numeric
  RPC IDs and cancellation of pending initialization on Close.
