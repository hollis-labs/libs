# Migration: go-sqlite-backup -> libs/util/sqlitebackup

`github.com/hollis-labs/go-sqlite-backup` moved into the libs monorepo as `util/sqlitebackup/`, with its full git history (6 commits, 2026-09-29 to 2026-09-30; source HEAD `7d232880ecdb`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-sqlite-backup` | `github.com/hollis-labs/libs/util/sqlitebackup` | `sqlitebackup` |
| `github.com/hollis-labs/go-sqlite-backup/examples/hello` | `github.com/hollis-labs/libs/util/sqlitebackup/examples/hello` |  (command) |

`go get github.com/hollis-labs/libs/util/sqlitebackup@<version>` replaces `go get github.com/hollis-labs/go-sqlite-backup@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Internal requirement.** It required `go-sqlite` as a separate module; that is now an ordinary import of `github.com/hollis-labs/libs/util/sqlite` inside the same module.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
