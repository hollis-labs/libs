# Migration: go-sandbox -> substrate/harness

`github.com/hollis-labs/go-sandbox` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (34 commits, 2026-04-27 to 2026-10-01; source HEAD `263a01e3653c`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-sandbox/sandbox` | `github.com/hollis-labs/substrate/harness/sandbox` | `sandbox` |
| `github.com/hollis-labs/go-sandbox/examples/clockwork_integration` | `github.com/hollis-labs/substrate/harness/sandbox/examples/clockwork_integration` |  (command) |
| `github.com/hollis-labs/go-sandbox/examples/mux_integration` | `github.com/hollis-labs/substrate/harness/sandbox/examples/mux_integration` |  (command) |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-sandbox@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.2.1 v0.3.0 v0.4.0 v0.4.1 v0.5.0 v0.5.1 v0.6.0) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held no Go package; `sandbox` is now `harness/sandbox/` itself, next to `atomicfile` and `pathsafe`.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `gopkg.in/yaml.v3` v3.0.1 (same version) and `github.com/hollis-labs/go-safefs` v0.1.0. go-safefs is now `harness/sandbox/atomicfile` and `harness/sandbox/pathsafe`, so `sandbox` builds against the source at go-safefs `main` (v0.1.0 plus 2 commits).
- **Files not carried to the new location** (git history still has them): `.gitignore`, `go.mod`, `go.sum`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is CW-20261003-0135.
