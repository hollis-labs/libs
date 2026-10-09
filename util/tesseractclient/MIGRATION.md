# Migration: go-tesseract-client -> libs/util/tesseractclient

`github.com/hollis-labs/go-tesseract-client` moved into the libs monorepo as `util/tesseractclient/`, with its full git history (9 commits, 2026-09-29 to 2026-09-30; source HEAD `e2eeb4b11934`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-tesseract-client` | `github.com/hollis-labs/libs/util/tesseractclient` | `tesseract` |
| `github.com/hollis-labs/go-tesseract-client/tesseracttest` | `github.com/hollis-labs/libs/util/tesseractclient/tesseracttest` | `tesseracttest` |

`go get github.com/hollis-labs/libs/util/tesseractclient@<version>` replaces `go get github.com/hollis-labs/go-tesseract-client@<version>`; the new module is `github.com/hollis-labs/libs/util`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/libs/util` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the package first ships in `util/v0.2.0`.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **Package name.** The package clause stays `tesseract`, as it was before the move; only the directory is named `tesseractclient`. Code that imported it under the name `tesseract` keeps working with the new path.
- **API.** No symbol was renamed or changed by the move.
- **Subpackages.** `tesseracttest` keeps its suffix: `.../go-tesseract-client/tesseracttest` is now `.../util/tesseractclient/tesseracttest`.
- **Dependency versions.** The lib required nothing outside the standard library, so `util/go.mod` gained no requirements.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
