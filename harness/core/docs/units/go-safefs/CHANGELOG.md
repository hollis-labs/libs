# Changelog

All notable changes to go-safefs are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Write the entry for a release here BEFORE cutting its tag: the release workflow
refuses a tag whose CHANGELOG has no heading for it.

## v0.1.0 — 2026-09-29

### Added

- `pathsafe`: `ResolveUnder` and `EscapeError`, lifted verbatim from Nanite's `internal/pathsafe` (identical to go-sandbox's copy apart from one comment).
- `atomicfile`: `WriteFile`, `NewWriter` and `Writer`, lifted from Nanite's `internal/fsutil` and renamed (`AtomicWriteFile` to `WriteFile`, `AtomicWriter` to `NewWriter`). `NewWriter` now returns the concrete `*Writer` instead of `io.WriteCloser`, and error strings use the `atomicfile:` prefix.
- Seed test suites ported with mechanical renames; godoc examples for both packages.
- `TestResolveUnder_SiblingWithRootAsPrefixIsRefused`: a symlink under the root that points at a sibling directory whose name starts with the root's name (`/x/root-evil`) must be refused. The ported seed suite had no such case; a raw string-prefix `isUnder` passed it.
- **Deviation from "verbatim": injection seams in `atomicfile`.** Three unexported package-level variables (`writeTemp`, `syncFile`, `syncDir`) now stand between the code and `(*os.File).Write` / `Sync`; their defaults are the exact calls the seed made, so production behaviour is unchanged. Reason: the seed's own regression tests for the parent-directory fsync and the short-write check did not regression-test anything (they asserted the success path and an error-string literal, and removing either step left the suite green). New tests in `atomicfile/seams_test.go` observe the temp-file fsync happening before publish, the parent-directory fsync happening after the rename on the right directory, fsync failures being reported (target not published, temp removed), and a short write being an error with the original untouched. Mutation-checked: removing the parent-dir fsync, the short-write check, the temp fsync (in `WriteFile` and in `Writer.Close`) or swapping the parent-dir fsync before the rename each turns a test red. The seed's weak tests are kept as ported.
- **Security fix (deviation from verbatim, found in review): a dangling symlink as the FINAL path component escaped `pathsafe.ResolveUnder`.** For `root/link` where `link -> /outside/newfile` and the target does not exist, `EvalSymlinks` reports not-exist and the seed's upward walk began at the link's parent, so it never looked at the link itself: `ResolveUnder(root, "link")` returned `root/link` with no error, and a caller that then created that path wrote to `/outside/newfile`. `resolveSymlinksBestEffort` now `Lstat`s the path first and, if it is a symlink, follows it by hand (relative targets resolve against the link's real directory; chains are followed up to 40 hops; a cycle fails) and the destination is what `isUnder` judges. A dangling link whose target stays inside the root is still allowed. New tests in `pathsafe/dangling_test.go` (absolute, missing-parent, chained and relative outside targets, inside targets, cycle) all fail without the fix. The same defect exists in Nanite's and go-sandbox's copies of `pathsafe`.
- **Behaviour change in `atomicfile.Writer`: a failed `Write` is remembered.** The first `Write` error (or an `io.ErrShortWrite` when fewer bytes were stored with no error) is recorded; later `Write`s return it without writing, and `Close` removes the temp file and returns it instead of fsyncing and renaming a truncated file over the target. Previously `w := NewWriter(...); defer w.Close(); w.Write(big)` with the error ignored, or an `io.Copy` that returned early, published partial content. `Writer.Write` now goes through the same `writeTemp` seam as `WriteFile`. Tests: `TestClosePublishesNothingAfterAFailedWrite`, `TestShortWriteOnAWriterAbortsTheClose`. Documented on `Close`: the parent-directory fsync runs after the rename, so its error is returned although the new content is in place, and `Close` after `Abort` returns nil.
- Initial scaffold.
