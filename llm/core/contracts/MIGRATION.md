# Migration: agent-contracts-leaf -> substrate/llm-core/contracts

`github.com/hollis-labs/agent-contracts-leaf` moved into the substrate monorepo as `llm-core/contracts/`, with its full git history (12 commits, 2026-09-29 to 2026-09-30; source HEAD `5d6a5db389fb`). The standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/agent-contracts-leaf` | `github.com/hollis-labs/substrate/llm-core/contracts` | `contracts` |
| `github.com/hollis-labs/agent-contracts-leaf/capabilities` | `github.com/hollis-labs/substrate/llm-core/contracts/capabilities` | `capabilities` |
| `github.com/hollis-labs/agent-contracts-leaf/runtimes` | `github.com/hollis-labs/substrate/llm-core/contracts/runtimes` | `runtimes` |
| `github.com/hollis-labs/agent-contracts-leaf/examples/hello` | `github.com/hollis-labs/substrate/llm-core/contracts/examples/hello` |  (command) |

`go get github.com/hollis-labs/substrate/llm-core/contracts@<version>` replaces `go get github.com/hollis-labs/agent-contracts-leaf@<version>`; the new module is `github.com/hollis-labs/substrate/llm-core`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/llm-core` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0) were not carried over; the first release of the new module will be tagged `llm-core/v0.1.0` (not tagged yet).
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages and the history in `CHANGELOG.md` are left as written.
- **Package name.** The root package clause changed from `agentcontracts` to `contracts` (`contracts_test` for external tests) to match its directory. A caller that already imports it under the alias `agentcontracts` (the known importers already do) keeps compiling after only the import path changes; an unaliased import is now referred to as `contracts.`.
- **Removed: `status.go`.** `InstanceStatus` (the seven lifecycle values), `WaitingReason`, `StoppedReason`, `StoppedCause`, `StoppedDetail`, `A2ATaskState` and `InstanceStatus.ToA2A` are not part of `llm-core/contracts`: the old seven-state status is superseded by the mesh contracts. The file and its tests stay in the history of this directory. No known consumer used these symbols.
- **Subpackages.** `capabilities` and `runtimes` keep their package names and move to `contracts/capabilities` and `contracts/runtimes`.
- **API.** Apart from the removal above, no symbol was renamed or changed by the move.
- **Dependency versions.** Everything this lib required is at the same version it had before.
- **Files not carried to the new location** (git history still has them): `.github`, `lefthook.yml`, `.folio.yaml`, `go.mod`. Also `status.go` and `status_test.go`, see above.
