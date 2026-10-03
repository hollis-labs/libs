# Migration: go-worktree -> libs/util/worktree

`github.com/hollis-labs/go-worktree` moved into the libs monorepo as `util/worktree/`, with its full git history (8 commits, 2026-09-29 to 2026-09-30; source HEAD `6b7979916922`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-worktree` | `github.com/hollis-labs/libs/util/worktree` | `worktree` |
| `github.com/hollis-labs/go-worktree/examples/hello` | `github.com/hollis-labs/libs/util/worktree/examples/hello` |  (command) |
| `github.com/hollis-labs/go-worktree/ghmerged` | `github.com/hollis-labs/libs/util/worktree/ghmerged` | `ghmerged` |

`go get github.com/hollis-labs/libs/util/worktree@<version>` replaces `go get github.com/hollis-labs/go-worktree@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Subpackages.** `ghmerged` keeps its suffix: `.../go-worktree/ghmerged` is now `.../util/worktree/ghmerged`.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
