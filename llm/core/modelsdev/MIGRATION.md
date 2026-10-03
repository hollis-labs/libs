# Migration: go-modelsdev -> substrate/llm-core/modelsdev

`github.com/hollis-labs/go-modelsdev` moved into the substrate monorepo as `llm-core/modelsdev/`, with its full git history (13 commits, 2026-04-25 to 2026-10-03; source HEAD `4169934429b5`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-modelsdev/modelsdev` | `github.com/hollis-labs/substrate/llm-core/modelsdev` | `modelsdev` |
| `github.com/hollis-labs/go-modelsdev/examples/lookup` | `github.com/hollis-labs/substrate/llm-core/modelsdev/examples/lookup` |  (command) |
| `github.com/hollis-labs/go-modelsdev/examples/refresher` | `github.com/hollis-labs/substrate/llm-core/modelsdev/examples/refresher` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/modelsdev@<version>` replaces `go get github.com/hollis-labs/go-modelsdev@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **Package directory.** The old module's root held no Go package; the package lived in its `modelsdev/` directory. That directory is now `llm-core/modelsdev/` itself, next to the old repository's README, AGENTS.md, LICENSE and `examples/`. The old import path `github.com/hollis-labs/go-modelsdev/modelsdev` is the new `github.com/hollis-labs/substrate/llm-core/modelsdev`, and the old module path alone (`go get github.com/hollis-labs/go-modelsdev`) in the README is now the new package path.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
