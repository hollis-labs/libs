# Changelog

All notable changes to the `mesh` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`mesh/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- `agentdef`: strict version-2 definition parsing and validation, namespaced
  extension negotiation, semantic and artifact digests, and conformance fixtures.
  Harness permissions reference profile names; content pins are checked for
  syntax and verified by the host resolver.
- Empty module skeleton: `go.mod` and a package doc. No API yet.
