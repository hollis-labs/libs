# Migration: go-apppaths -> libs/util/apppaths

`github.com/hollis-labs/go-apppaths` moved into the libs monorepo as `util/apppaths/`, with its full git history (13 commits, 2026-05-17 to 2026-09-30; source HEAD `361065990d62`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-apppaths/examples/ownerperms` | `github.com/hollis-labs/libs/util/apppaths/examples/ownerperms` |  (command) |
| `github.com/hollis-labs/go-apppaths/examples/printpaths` | `github.com/hollis-labs/libs/util/apppaths/examples/printpaths` |  (command) |
| `github.com/hollis-labs/go-apppaths/paths` | `github.com/hollis-labs/libs/util/apppaths` | `paths` |

`go get github.com/hollis-labs/libs/util/apppaths@<version>` replaces `go get github.com/hollis-labs/go-apppaths@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0) were not carried over; the first release of the new module will be tagged `util/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Package name.** The directory is now `apppaths` but the package clause is still `paths`, as before. Import it with an explicit alias, `paths "github.com/hollis-labs/libs/util/apppaths"`; the rewrite added the alias wherever an import had none. The old `paths/` directory was lifted to the top of `util/apppaths/`, so what used to be `paths/layout.go` is now `util/apppaths/layout.go`.
- **Dependency versions raised.** One `go.mod` can require only one version of a dependency, so the highest one any imported lib asked for wins. For this lib that means:
  - `golang.org/x/sys`: v0.26.0 -> v0.48.0
- **Files not carried to the new location** (git history still has them): `.github`, `.folio.yaml`, `go.mod`, `go.sum`.
