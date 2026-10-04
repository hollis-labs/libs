# Changelog

All notable changes to the `harness` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`harness/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

The harness module contains the packages of eleven former Hollis Labs modules, moved in with their git history at their final homes, and a stdio process shim library with its `cairn-shim` executable, written in this module. The old modules' release tags were not carried over. The module requires `llm-core` v0.1.0 and `mesh` v0.1.0 and builds with Go 1.26.6. It has no released version and no tag yet. Its package layout is interim and will move again with the restructure.

### Known issues

- `TestNativeTurnOrder_CodexJSONRPCStdio` (`adapters/wrapper`) is skipped. Since agentkit v0.23.0 (commit 433cd74) both the wrapper's own turn lifecycle derivation and agentkit's Manager end a Codex JSON-RPC turn, so the test sees a second, empty turn lifecycle. The wrapper passes it against agentkit v0.21.0, the version it pinned, so a consumer that pins go-agent-wrapper v0.28.0 together with an older agentkit does not see the problem. Fixing it is part of the restructure that puts the wrapper on sessions.
- `TestSandbox_OutsideWorkspaceReadBlocked` (`sandbox`) is skipped unconditionally on Linux: the skip says the test needs additional bind configuration, and it has said so since the sandbox was first extracted, before this move. So the test suite does not verify that a sandboxed process on Linux cannot read a denied path outside its workspace. That is not known to be broken; it is not verified. On macOS the same test runs only when `~/.ssh` exists. The other Linux sandbox isolation tests run and pass in CI: network isolation, loopback, protected paths and the filesystem-allowlist parity tests.

### Added

- The packages of six former Hollis Labs modules, moved in with their git history at their final homes: `sandbox/atomicfile` and `sandbox/pathsafe` (from `go-safefs`), `interception/permission` with `pathgrants` and `summary` (from `go-permission`), `workspace/materialize` with `artifact` (from `go-materialize`), `interception/filters/{classify,directive,event,normalize,repair}` (from `go-harness-filters`), `interception/egress` (from `go-egress-proxy`) and `adapters/runtimeevents` (from `go-runtime-events`). No package clause or symbol was renamed. Each unit's README, AGENTS.md, CHANGELOG.md, LICENSE and docs are under `docs/units/<old-name>/`, with a `MIGRATION.md` listing old and new import paths. The old modules' release tags were not carried over.
- Requirement: `gopkg.in/yaml.v3` v3.0.1 (from `go-permission`).
- The packages of three more former modules, moved in with their git history at their final homes: `sandbox` (from `go-sandbox`), `adapters/provider` with `provider/events`, `adapters/providertest`, `adapters/registry` and `adapters/layout` with `gen` and `layouttest` (from `go-providers`), and `runner` with `internal/stubcli` (from `go-runner`). No package clause or symbol was renamed. Each unit's README, AGENTS.md, CHANGELOG.md, LICENSE and docs are under `docs/units/<old-name>/` with a `MIGRATION.md`; the layout documents are in `adapters/layout/docs/`.
- Requirements: `github.com/creack/pty` v1.1.24 (from `go-providers` and `go-runner`) and `github.com/hollis-labs/substrate/llm-core` v0.1.0, which replaces the old `go-llm-types`, `go-llm-contracts` and `agent-contracts-leaf` requirements.
- The packages of `agentkit`, moved in with their git history: `adapters/agentsessions` (with `compliance`), `adapters/{checkpoint,loopback,runtimebind,sessionkit,turn}`, `agentlaunch` (with `launcher`, `matrix`, `parity`, `catalog`, `contexthook` and `sessionshim`), `workspace/{bootdir,providerplant}`, `agentcontext` (with `resolvers` and `skills`) and `broker`. `agentcontext`, `contexthook`, `catalog`, `sessionshim` and `broker` stay in this module for now. The umbrella root, the `agentruntime` facade and `agentruntime/smoke` are not carried. No package clause or symbol was renamed. Its README, AGENTS.md, CHANGELOG.md, LICENSE and docs are under `docs/units/agentkit/` with a `MIGRATION.md`.
- The packages of `go-agent-wrapper`, moved in with their git history at their final homes: `adapters` (with `acp`, `activity`, `launch`, `turnoutput`, `wrapper` and the provider subpackages `claude`, `claudeacp`, `codex`, `codexacp`, `copilotacp`, `opencode`, `opencodeacp`, `piacp`), `interception/{classifybridge,policy,filters}`, `workspace/plant`, `sandbox/{wrapper,snapshot}` and `internal/{childoutput,closegate,sidebyside,testgate}`. No package clause or symbol was renamed. Its README, AGENTS.md, CHANGELOG.md, LICENSE, ROADMAP.md and docs are under `docs/units/go-agent-wrapper/` with a `MIGRATION.md`.
- Stdio process shim library and separate `cairn-shim` executable with a private
  authenticated Unix controller, durable shared event journal, replay, bounded
  immediate input/signals, generation-use pins and launch-time limits.
- Fake-child acceptance tests for detach/reconnect, framing, durability,
  idempotency, controller fencing and process-group cleanup.

### Changed

- `adapters/layout/gen` writes `adapters/layout/layout.json` and `adapters/layout/docs/LAYOUT.md`, the files' new locations relative to the module root (the generator's two path constants; it would otherwise have written `layout/layout.json` and `docs/LAYOUT.md` next to the module root).
- Code that pinned a sibling at a tag now builds against the sibling's source at its old repository's `main`: `runner` (was `go-providers` v0.26.0, `go-sandbox` v0.3.0, `go-llm-types` v0.3.0), `adapters/provider` (was `go-llm-contracts` v0.1.0, `go-permission` v0.1.0) and `sandbox` (was `go-safefs` v0.1.0).
- `agentkit`'s code, which pinned `go-materialize` v0.1.0, `go-permission` v0.1.0, `go-providers` v0.46.0, `go-runner` v0.8.2, `go-sandbox` v0.6.0 and `go-safefs` v0.1.0 at tags, builds against their packages in this module, and its llm-core requirements are `llm-core` v0.1.0.
- `go-agent-wrapper`'s code, which pinned `agentkit` v0.21.0 and `go-runtime-events` v0.2.1 and the other sibling modules at tags, builds against their packages in this module: `agentkit` v0.26.1, `go-runtime-events` v0.2.2, the rest at their old repositories' `main`; its llm-core requirements are `llm-core` v0.1.0.

### Fixed

- `sandbox`: the Linux loopback helper no longer fails with `operation not permitted` when bwrap has already brought `lo` up. With `--unshare-net`, bwrap 0.9.0 raises `lo` and then drops every capability from the sandboxed process, and the helper asked for the same change again, which needs `CAP_NET_ADMIN` even when nothing changes. It now writes the interface flags only when `lo` is down; the payload gets no new capability. The Linux sandbox tests had not run in CI before the CI workflow installed bubblewrap; five loopback tests failed for this reason.
- Pull replay beyond bounded client queues without dropping healthy readers;
  keep ACK watermarks outside the stream to avoid event feedback.
- Bound pipe draining after leader exit, preserve signal evidence, and fence
  group signalling after reaping. Recover interrupted journal header/identity
  creation and reject oversized provenance without stopping a healthy child.
- Journal connection attach/detach and publish protocol negotiation details;
  keep slow response writes outside the controller operation lock.
- Recheck shutdown after durable attach before registering either connection
  role, and bound untrusted refusal metadata with truncation evidence.
- Include underlying launch errors on stderr; describe bounded drain/grace
  intervals accurately and wait for disconnect processing in negotiation tests.
