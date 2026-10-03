# Changelog

All notable changes to the `harness` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`harness/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- Stdio process shim library and separate `cairn-shim` executable with a private
  authenticated Unix controller, durable shared event journal, replay, bounded
  immediate input/signals, generation-use pins and launch-time limits.
- Fake-child acceptance tests for detach/reconnect, framing, durability,
  idempotency, controller fencing and process-group cleanup.
