# Migration: go-usage-ledger -> substrate/llm-core/usageledger

`github.com/hollis-labs/go-usage-ledger` moved into the substrate monorepo as `llm-core/usageledger/`, with its full git history (5 commits, 2026-09-29 to 2026-09-30; source HEAD `54f467d155fb`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-usage-ledger` | `github.com/hollis-labs/substrate/llm-core/usageledger` | `usageledger` |
| `github.com/hollis-labs/go-usage-ledger/examples/hello` | `github.com/hollis-labs/substrate/llm-core/usageledger/examples/hello` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/usageledger@<version>` replaces `go get github.com/hollis-labs/go-usage-ledger@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`.
