# Migration: go-webui -> libs/ui-go/webui

`github.com/hollis-labs/go-webui` moved into the libs monorepo as `ui-go/webui/`, with its full git history (8 commits, 2026-05-15 to 2026-09-30; source HEAD `400bed79a5f6`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-webui` | `github.com/hollis-labs/libs/ui-go/webui` | `webui` |
| `github.com/hollis-labs/go-webui/examples/embedded` | `github.com/hollis-labs/libs/ui-go/webui/examples/embedded` |  (command) |

`go get github.com/hollis-labs/libs/ui-go/webui@<version>` replaces `go get github.com/hollis-labs/go-webui@<version>`; the new module is `github.com/hollis-labs/libs/ui-go`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/ui-go` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0) were not carried over; the first release of the new module will be tagged `ui-go/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
