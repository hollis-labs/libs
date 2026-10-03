# Migration: go-directives -> libs/ui-go/directives

`github.com/hollis-labs/go-directives` moved into the libs monorepo as `ui-go/directives/`, with its full git history (10 commits, 2026-04-07 to 2026-09-30; source HEAD `aa991c5067d6`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-directives` | `github.com/hollis-labs/libs/ui-go/directives` | `directives` |
| `github.com/hollis-labs/go-directives/examples/parse` | `github.com/hollis-labs/libs/ui-go/directives/examples/parse` |  (command) |

`go get github.com/hollis-labs/libs/ui-go/directives@<version>` replaces `go get github.com/hollis-labs/go-directives@<version>`; the new module is `github.com/hollis-labs/libs/ui-go`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/ui-go` module (one `go.mod` for all of its packages). Release tags of the old module (v0.0.1 v0.0.2 v0.1.0) were not carried over; the first release of the new module will be tagged `ui-go/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
