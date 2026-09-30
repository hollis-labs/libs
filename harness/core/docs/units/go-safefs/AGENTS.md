# go-safefs

Filesystem safety primitives: path confinement under a root and crash-safe atomic writes.

It is not: a general filesystem utility library. It holds exactly two things, path confinement and atomic writes, and the mistake this repo attracts is growing a catch-all `fsutil`.

## Start Here

- `pathsafe/` and `atomicfile/` — the two sibling packages (no root package); each `doc.go` is that package's documentation.
- `README.md` — one runnable `go` fence per package; keep them compiling against the current API (`ExampleResolveUnder` and `ExampleWriteFile` guard signature drift).
- `.github/workflows/check.yml` — the full CI gate; `release.yml` refuses a tag with no CHANGELOG heading.

## Commands

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## Boundaries

- No `replace` directive in `go.mod` and no committed `go.work`: consumers cannot resolve either.
- Zero dependencies: `go.mod` has no `require` block and there is no `go.sum`. Do not add a dependency.
- No root package. `pathsafe` (confinement) and `atomicfile` (atomic writes) are unrelated contracts and must not import each other.
- No grab-bag helpers: no copy/walk/move/tree utilities, no `fsutil`-style catch-all. The `atomicfile` rename exists to stop that accumulation.
- Longest-existing-ancestor symlink resolution: `ResolveUnder` resolves symlinks on the longest ancestor that exists and re-joins the missing suffix, so a not-yet-created leaf is still validated. Guarded by `TestResolveUnder_Table` (cases `nonexistent leaf`, `nonexistent deep path`, `symlink escape`, `symlink escape subpath`) and `TestResolveUnder_NonexistentRoot`.
- `..` and symlink escapes must return `*EscapeError`. Guarded by `TestResolveUnder_Table` (`dotdot escape`, `dotdot mid-path`, `symlink escape*`, `null byte`).
- Sibling-prefix rejection: `/root-evil` is not under `/root`; `isUnder` must compare path components (via `filepath.Rel`), never raw string prefixes. The seed's table had no such case (replacing `isUnder` with `strings.HasPrefix` left it green); `TestResolveUnder_SiblingWithRootAsPrefixIsRefused` in `pathsafe/sibling_test.go` now guards it (goes red under that mutation).
- fsync ordering in `atomicfile`: temp file fsync, then chmod, then rename, then parent-directory fsync (`syncParentDir`). Never publish before the temp file is synced; never skip the parent-dir sync. Guarded through the `writeTemp`/`syncFile`/`syncDir` seams by `atomicfile/seams_test.go` (mutation-checked: each step's removal, and the parent-dir/rename order swap, turns a test red). The seed's own `..._ParentDirFsync` and `..._ShortWriteSynthesizesError` tests are vacuous and are kept only as ported. Tests that replace a seam must not run in parallel.
- The temp file is removed on every error path, and `Writer.Abort` after `Close` is a no-op. Guarded by `TestAtomicWriteFile_NoPartialOnError`, `TestAtomicWriter_AbortLeavesNoTemp`, `TestAtomicWriter_AbortAfterCloseIsNoOp`.
- The source is lifted from `apps/nanite/internal/{pathsafe,fsutil}`. Behavior stays identical to the seed unless a CHANGELOG entry says otherwise; do not "improve" the lifted logic casually, it is security-relevant.
