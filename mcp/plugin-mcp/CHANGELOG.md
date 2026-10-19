# Changelog

All notable changes to the `plugin-mcp` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`plugin-mcp/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- `plugin-hooks`: the host-owned hook and filter engine (registry, catalog, execution, breaker, observability, remote scheduling, `hookstest` conformance harness, TypeScript reference in `plugin-hooks/ts`) and its SDK `bridge`, imported with history from the former `plugin-hooks` repository (whose `v0.1.0` tag is not carried). Import paths are now `github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-hooks[/...]`. The bridge tests that replay against a prepared SDK copy skip unless `SDK_TEST_SOURCE` is set (`plugin-hooks/bridge/scripts/prepare.sh`; the script's new layout is untested).

## [0.5.0] - 2026-10-17

### Added

- `go-mcp/toolmw`: adapts the `core/util/mw` middleware contract to go-mcp tool handlers. `Stack.Middleware` returns one `server.ToolMiddleware` for `server.WithToolMiddleware`; members are built once per tool at registration and can be limited to some tools; `Wrap` adapts existing go-mcp tool middleware (`StrictArgs`, `ValidateSchema`). Standard set: `Recover`, `Audit`, `Timeout` (per-tool overrides), `Authorize` and `DenyDestructive`. A fault or recovered panic reaches the client as a generic `internal` tool error, never its own text.

### Changed

- The module now requires `github.com/hollis-labs/libs/core/util` (for `mw`). Requires `core/util` v0.6.0.

## [0.4.0] - 2026-10-16

### Added

- `go-mcp/server`: `RecoverPanics` (tool middleware) and `RecoverPanicsReceiving` (SDK receiving middleware) recover a panic in a tool or request handler through `core/panics`. The caller gets an `internal_error` `budget.ToolError` (or a JSON-RPC internal error) carrying only the panic ref; the redacted value and stack go to the guard's reporter and the server keeps serving. `WithPanicGuard(*panics.Guard)` picks the guard.

### Changed

- **Behavior change:** `server.NewServer` installs the panic guard as the outermost middleware by default. `WithoutPanicGuard()` opts out for a caller that recovers panics itself.
- A recovered panic no longer puts its value in error text: `guard.Guarded`, the plugin-host connection reader and the subprocess runtime's unload error now return `internal error (ref pnc_...)`. Callers that matched the old `"<subject> panicked: <value>"` text must use the ref or `errors.As(err, *panics.Error)`. The subprocess handler-panic JSON-RPC message stays `panic: <value>` (fixed by the v1/v2 conformance transcripts shared with the TS SDK).
- Requires `core/panics` v0.1.0 (and `go 1.26.9`, which it requires).

## [0.3.0] - 2026-10-15

### Added

- `manifest.Manifest.Kind` (`connector`, `provider`, `module`; optional, closed set) with `manifest.ValidKind` and the `KindConnector`, `KindProvider` and `KindModule` constants. An unset kind is not written, so existing manifests and golden vectors are unchanged.

## [0.2.0] - 2026-10-09

- Module path is now `github.com/hollis-labs/libs/mcp/plugin-mcp` (was `github.com/hollis-labs/libs/plugin-mcp`); the previous path and its `plugin-mcp/vX.Y.Z` tags no longer exist. Tags are `mcp/plugin-mcp/vX.Y.Z`. No code change besides the path.
### Added

- Opt-in acknowledged GrantSet renewal in the Go and TypeScript SDKs, preserving
  exact authority and live runtime identity while replacing finite lease metadata.
  Future reverse clients receive the accepted snapshot; existing clients and calls
  retain their original deadline ceilings. Expiry, revocation and uncertain
  acknowledgement fail closed. Host-owned opaque descriptor scopes remain distinct
  from the shared normalized authority envelope.

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

- Make reverse-ID replay and descendant-cancellation fixtures observe the actual
  terminal ownership boundary, preserving duplicate rejection and sibling isolation.

## [0.1.0] - 2026-10-07

### Added

- History-preserving imports of plugin-sdk, plugin-host, mcp-host, go-mcp,
  go-mcp-sanitize and api-projection under one module.
- Final Go import paths under `github.com/hollis-labs/libs/mcp/plugin-mcp`,
  with each original package root retained and npm package names unchanged.
- Original dependency requirements and MIT licenses retained. Plugin hooks
  remain a separately released dependency.
- Protocol-2 inprocess MCP startup with trusted owner-supplied initialization,
  fresh per-attempt generations, verified Init agreement before Load, SDK numeric
  RPC IDs and cancellation of pending initialization on Close.
