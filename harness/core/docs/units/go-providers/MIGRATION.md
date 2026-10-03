# Migration: go-providers -> substrate/harness

`github.com/hollis-labs/go-providers` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (163 commits, 2026-04-07 to 2026-10-02; source HEAD `1da9e81038af`). Every package sits at its final home in the harness layout; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-providers/layout` | `github.com/hollis-labs/substrate/harness/adapters/layout` | `layout` |
| `github.com/hollis-labs/go-providers/layout/gen` | `github.com/hollis-labs/substrate/harness/adapters/layout/gen` |  (command) |
| `github.com/hollis-labs/go-providers/layout/layouttest` | `github.com/hollis-labs/substrate/harness/adapters/layout/layouttest` | `layouttest` |
| `github.com/hollis-labs/go-providers/provider` | `github.com/hollis-labs/substrate/harness/adapters/provider` | `provider` |
| `github.com/hollis-labs/go-providers/provider/events` | `github.com/hollis-labs/substrate/harness/adapters/provider/events` | `events` |
| `github.com/hollis-labs/go-providers/examples/claude_bare` | `github.com/hollis-labs/substrate/harness/adapters/provider/examples/claude_bare` |  (command) |
| `github.com/hollis-labs/go-providers/examples/codex_bootdir` | `github.com/hollis-labs/substrate/harness/adapters/provider/examples/codex_bootdir` |  (command) |
| `github.com/hollis-labs/go-providers/examples/opencode_bootdir` | `github.com/hollis-labs/substrate/harness/adapters/provider/examples/opencode_bootdir` |  (command) |
| `github.com/hollis-labs/go-providers/hack/capturefixtures` | `github.com/hollis-labs/substrate/harness/adapters/provider/hack/capturefixtures` |  (command) |
| `github.com/hollis-labs/go-providers/providertest` | `github.com/hollis-labs/substrate/harness/adapters/providertest` | `providertest` |
| `github.com/hollis-labs/go-providers/registry` | `github.com/hollis-labs/substrate/harness/adapters/registry` | `registry` |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/go-providers@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.0.1 v0.1.0 v0.2.0 v0.2.1 v0.3.0 v0.4.0 v0.5.0 v0.5.1 v0.6.0 v0.7.0 v0.8.0 v0.8.1 v0.8.2 v0.9.0 v0.9.1 v0.10.0 v0.11.0 v0.12.0 v0.13.0 v0.14.0 v0.15.0 v0.16.0 v0.16.1 v0.16.2 v0.17.0 v0.17.1 v0.18.0 v0.19.0 v0.20.0 v0.21.0 v0.22.0 v0.23.0 v0.24.0 v0.25.0 v0.26.0 v0.27.0 v0.28.0 v0.29.0 v0.30.0 v0.31.0 v0.32.0 v0.33.0 v0.34.0 v0.34.1 v0.35.0 v0.36.0 v0.37.0 v0.38.0 v0.39.0 v0.39.1 v0.39.2 v0.39.3 v0.40.0 v0.40.1 v0.41.0 v0.42.0 v0.43.0 v0.44.0 v0.45.0 v0.46.0) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** Each package is at the home ADR 0055 gives it. No package clause was renamed, so no importer needs an alias for the move. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held no Go package. `provider`, `registry`, `providertest` and `layout` are now below `harness/adapters/`; the layout documents `docs/LAYOUT.md` and `docs/HARNESS-DISCOVERY.md` sit in `harness/adapters/layout/docs/` next to the generated `layout.json`. The layout generator wrote its two files to paths relative to the module root; its two path constants now name the new locations (`adapters/layout/layout.json`, `adapters/layout/docs/LAYOUT.md`), so `gen -check` and `go generate` work in the new place. This is the one edit in this move that is not an import-path rewrite.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `github.com/creack/pty` v1.1.24 and `gopkg.in/yaml.v3` v3.0.1 (same versions), `github.com/hollis-labs/go-llm-types` v0.5.1, `go-llm-contracts` v0.1.0, `agent-contracts-leaf` v0.3.0 and `go-permission` v0.1.0. The first three are now packages of `github.com/hollis-labs/substrate/llm-core` v0.1.0 (llmtypes at the same level; llmcontracts moves from v0.1.0 to go-llm-contracts `main`, v0.4.0 plus 1 commit; the contracts runtimes package unchanged); go-permission is now `harness/interception/permission` at its `main` (v0.1.0 plus 2 commits).
- **Files not carried to the new location** (git history still has them): `.github`, `.golangci.yml`, `.gitignore`, `go.mod`, `go.sum`.
- **Not done here.** Restructuring into the target layout (merges, splits, renames) is CW-20261003-0135.
