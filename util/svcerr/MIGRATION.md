# Migration: go-svcerr -> libs/util/svcerr

`github.com/hollis-labs/go-svcerr` moved into the libs monorepo as `util/svcerr/`, with its full git history (7 commits, 2026-09-29 to 2026-09-30; source HEAD `27bf0cd5cb23`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-svcerr` | `github.com/hollis-labs/libs/util/svcerr` | `svcerr` |

`go get github.com/hollis-labs/libs/util/svcerr@<version>` replaces `go get github.com/hollis-labs/go-svcerr@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
