# Migration: go-harness-filters -> substrate/harness

`github.com/hollis-labs/go-harness-filters` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (7 commits, 2026-05-26 to 2026-09-30; source HEAD `b9d573e42527`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-harness-filters/classify` | `github.com/hollis-labs/substrate/harness/interception/filters/classify` | `classify` |
| `github.com/hollis-labs/go-harness-filters/directive` | `github.com/hollis-labs/substrate/harness/interception/filters/directive` | `directive` |
| `github.com/hollis-labs/go-harness-filters/event` | `github.com/hollis-labs/substrate/harness/interception/filters/event` | `event` |
| `github.com/hollis-labs/go-harness-filters/normalize` | `github.com/hollis-labs/substrate/harness/interception/filters/normalize` | `normalize` |
| `github.com/hollis-labs/go-harness-filters/repair` | `github.com/hollis-labs/substrate/harness/interception/filters/repair` | `repair` |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-harness-filters@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.1.1) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held no Go package; its five packages are now below `harness/interception/filters/`.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this unit required is at the same version it had before (it required nothing outside the standard library).
- **Files not carried to the new location** (git history still has them): `.github`, `.folio.yaml`, `.gitignore`, `go.mod`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is a separate task.
