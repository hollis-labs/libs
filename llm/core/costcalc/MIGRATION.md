# Migration: go-modelsdev-catalog-helpers -> substrate/llm-core/costcalc

`github.com/hollis-labs/go-modelsdev-catalog-helpers` moved into the substrate monorepo as `llm-core/costcalc/`, with its full git history (5 commits, 2026-09-29 to 2026-09-30; source HEAD `7266aa0eded0`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-modelsdev-catalog-helpers` | `github.com/hollis-labs/substrate/llm-core/costcalc` | `costcalc` |
| `github.com/hollis-labs/go-modelsdev-catalog-helpers/examples/hello` | `github.com/hollis-labs/substrate/llm-core/costcalc/examples/hello` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/costcalc@<version>` replaces `go get github.com/hollis-labs/go-modelsdev-catalog-helpers@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `github.com/hollis-labs/go-modelsdev` v0.2.0 and `github.com/hollis-labs/go-usage-ledger` v0.1.0. It now builds against `llm-core/modelsdev` (go-modelsdev at its `main`: v0.3.0 plus 3 commits, among them a blocking `Client.Run`, and `Refresh` now commits nothing once its context is cancelled) and `llm-core/usageledger` (go-usage-ledger at its `main`: v0.1.0 plus 2 commits, docs and tooling only). Everything else this lib required (`github.com/cenkalti/backoff/v5` v5.0.3) is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`, `go.sum`.
