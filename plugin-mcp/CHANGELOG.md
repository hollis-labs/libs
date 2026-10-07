# Changelog

All notable changes to the `plugin-mcp` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`plugin-mcp/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- History-preserving imports of plugin-sdk, plugin-host, mcp-host, go-mcp,
  go-mcp-sanitize and api-projection under one module.
- Final Go import paths under `github.com/hollis-labs/libs/plugin-mcp`,
  with each original package root retained and npm package names unchanged.
- Original dependency requirements and MIT licenses retained. Plugin hooks
  remain a separately released dependency.
