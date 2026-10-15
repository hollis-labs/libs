# Migration: go-runner -> substrate/harness

`github.com/hollis-labs/go-runner` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (28 commits, 2026-04-27 to 2026-10-01; source HEAD `8821af571a3e`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-runner/runner` | `github.com/hollis-labs/substrate/harness/runner` | `runner` |
| `github.com/hollis-labs/go-runner/examples/basic` | `github.com/hollis-labs/substrate/harness/runner/examples/basic` |  (command) |
| `github.com/hollis-labs/go-runner/examples/env-passthrough` | `github.com/hollis-labs/substrate/harness/runner/examples/env-passthrough` |  (command) |
| `github.com/hollis-labs/go-runner/internal/stubcli` | `github.com/hollis-labs/substrate/harness/runner/internal/stubcli` |  (command) |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-runner@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.6.0 v0.7.0 v0.8.0 v0.8.1 v0.8.2) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held no Go package; `runner` is now `harness/runner/` itself, with `internal/stubcli` and the examples below it. The test that builds the stub CLI named its package by the old import path in a string; that string was rewritten to the new path (the rewrite of import specs does not touch strings).
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `go-llm-types` v0.3.0, `go-providers` v0.26.0, `go-sandbox` v0.3.0 and `go-llm-contracts` v0.3.0 (and `github.com/creack/pty` v1.1.24, `gopkg.in/yaml.v3` v3.0.1 and test-only indirect pins). It now builds against llm-core v0.1.0, `harness/adapters/provider` (go-providers `main`, v0.46.0) and `harness/sandbox` (go-sandbox `main`, v0.6.0), so it moves up from the tags it pinned. Its test-only indirect pins (`github.com/kr/pretty`, `github.com/rogpeppe/go-internal`, `gopkg.in/check.v1`) are not needed by any package here.
- **Files not carried to the new location** (git history still has them): `.gitignore`, `go.mod`, `go.sum`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is a separate task.
