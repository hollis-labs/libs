# Changelog

All notable changes to the `routepolicy` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`security/routepolicy/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

Write the entry for a release here BEFORE cutting its tag.

## [Unreleased]

## [0.1.0] - 2026-10-17

### Added

- `Adapter`, which adapts a `policy.Evaluator` to the router's `Policy` seam in `core/util/router`. A `Mapper` projects a routing candidate to a `policy.Request`; `DefaultMapper` builds one from the subject's principal fields. The verdict is the evaluator's effective admission, with `WouldBlock` carried from its shadow mode; obligations and matched-policy provenance come through unchanged; a failure is a fixed classification (`pdp_failed`, `invalid_request`, `invalid_decision`, `audit_unavailable`, ...), never error text.
