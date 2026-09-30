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
- `TestResolveUnder_SiblingWithRootAsPrefixIsRefused`: a symlink under the root that points at a sibling directory whose name starts with the root's name (`/x/root-evil`) must be refused. The ported seed suite had no such case; a raw string-prefix `isUnder` passed it.
- **Deviation from "verbatim": injection seams in `atomicfile`.** Three unexported package-level variables (`writeTemp`, `syncFile`, `syncDir`) now stand between the code and `(*os.File).Write` / `Sync`; their defaults are the exact calls the seed made, so production behaviour is unchanged. Reason: the seed's own regression tests for the parent-directory fsync and the short-write check did not regression-test anything (they asserted the success path and an error-string literal, and removing either step left the suite green). New tests in `atomicfile/seams_test.go` observe the temp-file fsync happening before publish, the parent-directory fsync happening after the rename on the right directory, fsync failures being reported (target not published, temp removed), and a short write being an error with the original untouched. Mutation-checked: removing the parent-dir fsync, the short-write check, the temp fsync (in `WriteFile` and in `Writer.Close`) or swapping the parent-dir fsync before the rename each turns a test red. The seed's weak tests are kept as ported.
- Initial scaffold.
