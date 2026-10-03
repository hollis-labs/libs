# Migration: go-llm-contracts -> substrate/llm-core/llmcontracts

`github.com/hollis-labs/go-llm-contracts` moved into the substrate monorepo as `llm-core/llmcontracts/`, with its full git history (11 commits, 2026-05-09 to 2026-09-30; source HEAD `e9e1044e4c9f`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/go-llm-contracts` | `github.com/hollis-labs/substrate/llm-core/llmcontracts` | `llmcontracts` |
| `github.com/hollis-labs/go-llm-contracts/contracttest` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/contracttest` | `contracttest` |
| `github.com/hollis-labs/go-llm-contracts/examples/cacheable` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/examples/cacheable` |  (command) |
| `github.com/hollis-labs/go-llm-contracts/examples/circuitbreaker` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/examples/circuitbreaker` |  (command) |
| `github.com/hollis-labs/go-llm-contracts/examples/provider` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/examples/provider` |  (command) |
| `github.com/hollis-labs/go-llm-contracts/examples/ratelimited` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/examples/ratelimited` |  (command) |
| `github.com/hollis-labs/go-llm-contracts/examples/tokenratetracker` | `github.com/hollis-labs/substrate/llm-core/llmcontracts/examples/tokenratetracker` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/llmcontracts@<version>` replaces `go get github.com/hollis-labs/go-llm-contracts@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0 v0.4.0) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **API.** No symbol was renamed or changed by the move.
- **Dependency versions.** It required `github.com/hollis-labs/go-llm-types` v0.3.0. It now builds against `llm-core/llmtypes`, which is in the same module and is go-llm-types at its `main` (v0.5.1). The difference from v0.3.0 is additive: `Usage.CostUSD`, `StreamEvent.BlockID` and `Phase`, the phase constants, and a shared stop-reason vocabulary with `NormalizeStopReason`. Everything else this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `go.mod`, `go.sum`.
