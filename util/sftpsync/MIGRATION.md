# Migration: go-sftpsync -> libs/util/sftpsync

`github.com/hollis-labs/go-sftpsync` moved into the libs monorepo as `util/sftpsync/`, with its full git history (4 commits, 2026-09-17 to 2026-09-17; source HEAD `99191334db25`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-sftpsync` | `github.com/hollis-labs/libs/util/sftpsync` | `sftpsync` |
| `github.com/hollis-labs/go-sftpsync/examples/overssh` | `github.com/hollis-labs/libs/util/sftpsync/examples/overssh` |  (command) |
| `github.com/hollis-labs/go-sftpsync/examples/preview` | `github.com/hollis-labs/libs/util/sftpsync/examples/preview` |  (command) |

`go get github.com/hollis-labs/libs/util/sftpsync@<version>` replaces `go get github.com/hollis-labs/go-sftpsync@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.1.1) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `CLAUDE.md`, `go.mod`, `go.sum`.
