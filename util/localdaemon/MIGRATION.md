# Migration: go-localdaemon -> libs/util/localdaemon

`github.com/hollis-labs/go-localdaemon` moved into the libs monorepo as `util/localdaemon/`, with its full git history (5 commits, 2026-09-30 to 2026-09-30; source HEAD `d7f801a3b8a0`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-localdaemon` | `github.com/hollis-labs/libs/util/localdaemon` | `localdaemon` |
| `github.com/hollis-labs/go-localdaemon/examples/hello` | `github.com/hollis-labs/libs/util/localdaemon/examples/hello` |  (command) |

`go get github.com/hollis-labs/libs/util/localdaemon@<version>` replaces `go get github.com/hollis-labs/go-localdaemon@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
