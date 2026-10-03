# Migration: go-streamhub -> libs/ui-go/streamhub

`github.com/hollis-labs/go-streamhub` moved into the libs monorepo as `ui-go/streamhub/`, with its full git history (12 commits, 2026-09-29 to 2026-09-30; source HEAD `4a66ca692648`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-streamhub` | `github.com/hollis-labs/libs/ui-go/streamhub` | `streamhub` |
| `github.com/hollis-labs/go-streamhub/hubtest` | `github.com/hollis-labs/libs/ui-go/streamhub/hubtest` | `hubtest` |

`go get github.com/hollis-labs/libs/ui-go/streamhub@<version>` replaces `go get github.com/hollis-labs/go-streamhub@<version>`; the new module is `github.com/hollis-labs/libs/ui-go`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/ui-go` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `ui-go/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
