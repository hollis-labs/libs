# Migration: go-llm-types -> substrate/llm-core/llmtypes

`github.com/hollis-labs/go-llm-types` moved into the substrate monorepo as `llm-core/llmtypes/`, with its full git history (16 commits, 2026-05-09 to 2026-09-30; source HEAD `52f600927935`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-llm-types` | `github.com/hollis-labs/substrate/llm-core/llmtypes` | `llmtypes` |
| `github.com/hollis-labs/go-llm-types/examples/chatrequest` | `github.com/hollis-labs/substrate/llm-core/llmtypes/examples/chatrequest` |  (command) |
| `github.com/hollis-labs/go-llm-types/examples/slots` | `github.com/hollis-labs/substrate/llm-core/llmtypes/examples/slots` |  (command) |
| `github.com/hollis-labs/go-llm-types/examples/streamevent` | `github.com/hollis-labs/substrate/llm-core/llmtypes/examples/streamevent` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/llmtypes@<version>` replaces `go get github.com/hollis-labs/go-llm-types@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.5.1) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `go.mod`.
