# Migration: go-transportparity -> libs/util/transportparity

`github.com/hollis-labs/go-transportparity` moved into the libs monorepo as `util/transportparity/`, with its full git history (6 commits, 2026-09-29 to 2026-09-30; source HEAD `b5ccd1e8f72c`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-transportparity` | `github.com/hollis-labs/libs/util/transportparity` | `transportparity` |

`go get github.com/hollis-labs/libs/util/transportparity@<version>` replaces `go get github.com/hollis-labs/go-transportparity@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Internal requirement.** It required `go-svcerr` as a separate module; that is now an ordinary import of `github.com/hollis-labs/libs/util/svcerr` inside the same module.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
