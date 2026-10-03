# Changelog

All notable changes to the `mesh` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`mesh/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

## v0.1.0

### Added

- The complete, history-preserved packages from `go-messaging`,
  `go-federation`, `go-tether-client`, and `go-hitl`, including their tests,
  examples, contracts, and package documentation.

### Changed

- The packages now share the `github.com/hollis-labs/substrate/mesh` module.
  Imports move as follows; no consumer adoption is included in this release
  preparation:

  | Old import prefix | New import prefix |
  |---|---|
  | `github.com/hollis-labs/go-messaging` | `github.com/hollis-labs/substrate/mesh/messaging` |
  | `github.com/hollis-labs/go-federation` | `github.com/hollis-labs/substrate/mesh/federation` |
  | `github.com/hollis-labs/go-tether-client` | `github.com/hollis-labs/substrate/mesh/tetherclient` |
  | `github.com/hollis-labs/go-hitl` | `github.com/hollis-labs/substrate/mesh/hitl` |
- Federation and tether-client now import messaging as a package in the same
  module rather than requiring the former `go-messaging` module.
- Old repository tags are not carried into substrate; the module's first tag
  will be `mesh/v0.1.0` after approval.
