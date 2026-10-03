# Migration: go-permission -> substrate/harness

`github.com/hollis-labs/go-permission` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (12 commits, 2026-09-29 to 2026-09-30; source HEAD `141253149436`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-permission` | `github.com/hollis-labs/substrate/harness/interception/permission` | `permission` |
| `github.com/hollis-labs/go-permission/pathgrants` | `github.com/hollis-labs/substrate/harness/interception/permission/pathgrants` | `pathgrants` |
| `github.com/hollis-labs/go-permission/summary` | `github.com/hollis-labs/substrate/harness/interception/permission/summary` | `summary` |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-permission@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The package was the old module's root; it is now `harness/interception/permission/`, with `pathgrants` and `summary` below it.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `gopkg.in/yaml.v3` v3.0.1; the merged `harness/go.mod` carries the same version.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `.golangci.yml`, `.gitignore`, `go.mod`, `go.sum`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is CW-20261003-0135.
