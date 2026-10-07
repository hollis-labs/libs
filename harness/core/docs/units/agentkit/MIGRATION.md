# Migration: agentkit -> substrate/harness

`github.com/hollis-labs/agentkit` moved into the substrate monorepo as packages of the `github.com/hollis-labs/substrate/harness` module, with its full git history (160 commits, 2026-05-25 to 2026-10-02; source HEAD `ef077de4ad5d`). Current destinations below include the subsequent workspace consolidation; the standalone module is no longer where new work happens.

## Import paths

| Old import path | New import path | Package |
|---|---|---|
| `github.com/hollis-labs/agentkit/agentsessions` | `github.com/hollis-labs/substrate/harness/adapters/agentsessions` | `agentsessions` |
| `github.com/hollis-labs/agentkit/agentsessions/compliance` | `github.com/hollis-labs/substrate/harness/adapters/agentsessions/compliance` | `compliance` |
| `github.com/hollis-labs/agentkit/examples/go-agent-sessions/runner_session` | `github.com/hollis-labs/substrate/harness/adapters/agentsessions/examples/runner_session` |  (command) |
| `github.com/hollis-labs/agentkit/agentruntime/checkpoint` | `github.com/hollis-labs/substrate/harness/adapters/checkpoint` | `checkpoint` |
| `github.com/hollis-labs/agentkit/agentruntime/loopback` | `github.com/hollis-labs/substrate/harness/adapters/loopback` | `loopback` |
| `github.com/hollis-labs/agentkit/agentruntime/runtimebind` | `github.com/hollis-labs/substrate/harness/adapters/runtimebind` | `runtimebind` |
| `github.com/hollis-labs/agentkit/agentruntime/sessionkit` | `github.com/hollis-labs/substrate/harness/adapters/sessionkit` | `sessionkit` |
| `github.com/hollis-labs/agentkit/agentruntime/turn` | `github.com/hollis-labs/substrate/harness/adapters/turn` | `turn` |
| `github.com/hollis-labs/agentkit/agentcontext` | `github.com/hollis-labs/substrate/harness/agentcontext` | `agentcontext` |
| `github.com/hollis-labs/agentkit/agentcontext/resolvers` | `github.com/hollis-labs/substrate/harness/agentcontext/resolvers` | `resolvers` |
| `github.com/hollis-labs/agentkit/agentcontext/skills` | `github.com/hollis-labs/substrate/harness/agentcontext/skills` | `skills` |
| `github.com/hollis-labs/agentkit/agentlaunch` | `github.com/hollis-labs/substrate/harness/agentlaunch` | `agentlaunch` |
| `github.com/hollis-labs/agentkit/agentlaunch/catalog` | `github.com/hollis-labs/substrate/harness/agentlaunch/catalog` | `catalog` |
| `github.com/hollis-labs/agentkit/agentlaunch/contexthook` | `github.com/hollis-labs/substrate/harness/agentlaunch/contexthook` | `contexthook` |
| `github.com/hollis-labs/agentkit/examples/go-agent-launch/providerplant` | `github.com/hollis-labs/substrate/harness/agentlaunch/examples/providerplant` |  (command) |
| `github.com/hollis-labs/agentkit/examples/go-agent-launch/with-context` | `github.com/hollis-labs/substrate/harness/agentlaunch/examples/with-context` |  (command) |
| `github.com/hollis-labs/agentkit/agentlaunch/launcher` | `github.com/hollis-labs/substrate/harness/agentlaunch/launcher` | `launcher` |
| `github.com/hollis-labs/agentkit/agentlaunch/matrix` | `github.com/hollis-labs/substrate/harness/agentlaunch/matrix` | `matrix` |
| `github.com/hollis-labs/agentkit/agentlaunch/parity` | `github.com/hollis-labs/substrate/harness/agentlaunch/parity` | `parity` |
| `github.com/hollis-labs/agentkit/agentlaunch/sessionshim` | `github.com/hollis-labs/substrate/harness/agentlaunch/sessionshim` | `sessionshim` |
| `github.com/hollis-labs/agentkit/broker` | `github.com/hollis-labs/substrate/harness/broker` | `broker` |
| `github.com/hollis-labs/agentkit/examples/go-agent-broker/deterministic` | `github.com/hollis-labs/substrate/harness/broker/examples/deterministic` |  (command) |
| `github.com/hollis-labs/agentkit/agentruntime/bootdir` | (public writer removed; explicit artifact materialization) | — |
| `github.com/hollis-labs/agentkit/agentlaunch/providerplant` | `github.com/hollis-labs/substrate/harness/agentlaunch/planting` | `planting` |
| `github.com/hollis-labs/agentkit` | (not carried) | `agentkit` |
| `github.com/hollis-labs/agentkit/agentruntime` | (not carried) | `agentruntime` |
| `github.com/hollis-labs/agentkit/agentruntime/smoke` | (not carried) | `smoke` |

`go get github.com/hollis-labs/substrate/harness@<version>` replaces `go get github.com/hollis-labs/agentkit@<version>`; the new module is `github.com/hollis-labs/substrate/harness`.

## What changed

- **Module.** The code is now part of the `github.com/hollis-labs/substrate/harness` module (one `go.mod` for all of its packages). Release tags of the old module (v0.1.0 v0.2.0 v0.3.0 v0.4.0 v0.5.0 v0.5.1 v0.6.0 v0.6.1 v0.7.0 v0.8.0 v0.9.0 v0.10.0 v0.11.0 v0.11.1 v0.12.0 v0.12.1 v0.12.2 v0.13.0 v0.14.0 v0.14.1 v0.14.2 v0.15.0 v0.16.0 v0.17.0 v0.18.0 v0.19.0 v0.19.1 v0.20.0 v0.20.1 v0.20.2 v0.20.3 v0.20.4 v0.20.5 v0.20.6 v0.21.0 v0.21.1 v0.21.2 v0.21.3 v0.22.0 v0.23.0 v0.23.1 v0.24.0 v0.25.0 v0.26.0 v0.26.1) were not carried over; no `harness/vX.Y.Z` tag exists yet.
- **Placement.** The initial import preserved package clauses. Subsequent planting consolidation uses the `planting` package qualifier and the current destinations above. Examples and fixtures sit next to the package they exercise. The old repository's own files (README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP and docs) are in this directory.
- **Import paths** in code, documentation and tests were rewritten mechanically, whole path segments only. Links to the old repository's web pages, the history in `CHANGELOG.md` and prose that still describes the standalone repository are left as written.
- **Layout.** The old module's root held only a doc-only umbrella package. Packages that could move whole are at their ADR 0055 homes: `agentsessions` (with `compliance`) and the `agentruntime` packages `checkpoint`, `loopback`, `runtimebind`, `sessionkit` and `turn` are below `harness/adapters/`; the interim bootdir writer is removed and provider projection/preparation lives in `harness/agentlaunch/planting`; the `harness/workspace` root owns planning and authority-checked materialization; `agentlaunch` is `harness/agentlaunch`. `agentcontext` (with `resolvers`, `skills`), `agentlaunch/contexthook`, `agentlaunch/catalog`, `agentlaunch/sessionshim` and `broker` stay inside the harness module for now (`harness/agentcontext`, `harness/agentlaunch/...`, `harness/broker`); ADR 0055 sends agentcontext and contexthook to the agent module, broker to the mesh module and replaces catalog and sessionshim, in later changes. Not carried: the root package `agentkit`, the `agentruntime` facade (its `ModulePath` constant and test) and `agentruntime/smoke`; nothing imports them, and the history keeps them. `agentruntime/runtimekind` no longer exists in agentkit (deleted on 2026-10-01) and has no destination. `agentlaunch.Version` (v0.3.5, stamped into `Provenance.CompilerVersion`) and `agentcontext.Version` (v0.1.0-dev, into `Provenance.LibraryVersion`) still carry the standalone libraries' versions, and the provenance and schema strings (`agentkit.materialize.v1`, `go-providers`, the codex client name) are unchanged: behaviour is not touched by the move.
- **API.** The initial import preserved symbols. Subsequent consolidation requires explicit artifact authority and removes engine/side-writer overrides; see the current module CHANGELOG. Wrapper planting types are now `planting.PlantSpec`, `PlantResult` and `PlantHook`.
- **Dependency versions.** It required `github.com/creack/pty` v1.1.24, `gopkg.in/yaml.v3` v3.0.1 and `github.com/kr/text` v0.2.0 (indirect), the same versions the merged `harness/go.mod` carries. Its requirements on `go-llm-types` v0.5.1, `go-llm-contracts` v0.4.0 and `agent-contracts-leaf` v0.3.0 are now `github.com/hollis-labs/substrate/llm-core` v0.1.0 (go-llm-contracts at its `main`: v0.4.0 plus 1 commit). `go-materialize` v0.1.0, `go-permission` v0.1.0, `go-providers` v0.46.0, `go-runner` v0.8.2, `go-sandbox` v0.6.0 and `go-safefs` v0.1.0 are packages of this module, at their `main` (materialize and permission plus 1 and 2 commits, safefs plus 2; providers, runner and sandbox are at their tags).
- **Files not carried to the new location** (git history still has them): `.github`, `.folio.yaml`, `.gitignore`, `go.mod`, `go.sum`.
- **Adoption.** Original import history is retained. Current library consolidation does not migrate live consumers or establish native installed/runtime support.
