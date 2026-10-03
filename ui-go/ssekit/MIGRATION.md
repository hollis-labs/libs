# Migration: go-ssekit -> libs/ui-go/ssekit

`github.com/hollis-labs/go-ssekit` moved into the libs monorepo as `ui-go/ssekit/`, with its full git history (10 commits, 2026-09-29 to 2026-09-30; source HEAD `0dde2e1e83a7`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-ssekit` | `github.com/hollis-labs/libs/ui-go/ssekit` | `ssekit` |
| `github.com/hollis-labs/go-ssekit/conformance` | `github.com/hollis-labs/libs/ui-go/ssekit/conformance` | `conformance` |
| `github.com/hollis-labs/go-ssekit/examples/hello` | `github.com/hollis-labs/libs/ui-go/ssekit/examples/hello` |  (command) |
| `github.com/hollis-labs/go-ssekit/ssetest` | `github.com/hollis-labs/libs/ui-go/ssekit/ssetest` | `ssetest` |

`go get github.com/hollis-labs/libs/ui-go/ssekit@<version>` replaces `go get github.com/hollis-labs/go-ssekit@<version>`; the new module is `github.com/hollis-labs/libs/ui-go`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/ui-go` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0) were not carried over; the first release of the new module will be tagged `ui-go/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
