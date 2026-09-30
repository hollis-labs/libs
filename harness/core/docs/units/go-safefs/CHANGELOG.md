# Changelog

All notable changes to go-safefs are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## Unreleased

### Added

- `pathsafe`: `ResolveUnder` and `EscapeError`, lifted verbatim from Nanite's `internal/pathsafe` (identical to go-sandbox's copy apart from one comment).
- `atomicfile`: `WriteFile`, `NewWriter` and `Writer`, lifted from Nanite's `internal/fsutil` and renamed (`AtomicWriteFile` to `WriteFile`, `AtomicWriter` to `NewWriter`). `NewWriter` now returns the concrete `*Writer` instead of `io.WriteCloser`, and error strings use the `atomicfile:` prefix.
- Seed test suites ported with mechanical renames; godoc examples for both packages.
- Initial scaffold.
