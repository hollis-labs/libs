# Migration: go-materialize -> substrate/harness

`github.com/hollis-labs/go-materialize` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (9 commits, 2026-09-18 to 2026-09-30; source HEAD `4be03e996c0d`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-materialize/materialize` | `github.com/hollis-labs/substrate/harness/workspace/materialize` | `materialize` |
| `github.com/hollis-labs/go-materialize/artifact` | `github.com/hollis-labs/substrate/harness/workspace/materialize/artifact` | `artifact` |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-materialize@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held no Go package; `materialize` and `artifact` are now `harness/workspace/materialize/` and `harness/workspace/materialize/artifact/`.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this unit required is at the same version it had before (it required nothing outside the standard library).
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `.golangci.yml`, `.gitignore`, `go.mod`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is a separate task.
