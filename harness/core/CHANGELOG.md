# Changelog

All notable changes to the `harness` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`harness/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## v0.3.1

### Fixed

- Workspace artifact validation accepts private managed directories with authored
  modes `0700` and `0750`, preserving their modes through create, refresh and
  reconcile. The existing `0755` default, unsafe-mode refusal and inactive
  artifact-authority requirements are unchanged.

## v0.3.0

### Changed

- Move ordered context assembly, resolvers and skills to `substrate/agent/agentcontext`; move artifact-backed recipes and composers to the caller-owned `agentcomposition` package. Callers use their authoritative owners directly, without old-path aliases. Neutral boot inputs are unchanged.

## v0.2.0

- Added `cmd/cairn boot --resolved` with versioned resolved Input/HostInputDTO
  JSON. `--plan` returns a pure plan; default preparation preserves the typed
  missing-host-port refusal and full result. JSON does not construct host
  authority. The older Cairn module/binary and live launch paths are untouched.
- Added the named `catalog-auto` permission binding with explicit yolo posture
  and deny-rule bypass evidence. It requires caller-authorized yolo ceilings and
  refuses required deny enforcement; it does not implement native auto mode.
  All named bindings now carry `harness-permission-profiles-v2`; boot refuses
  stale resolved versions. Hosts must resolve and authorize the binding separately.
- Added `boot.Plan` and `boot.Prepare` for caller-resolved definitions, typed
  policy, explicit model/effort, neutral composed context and independently
  authorized host inputs. Canonical artifacts use the sole workspace engine;
  complete process argv uses existing provider conventions. Full apply results,
  source provenance, retained roots and obligations survive failures; artifact
  completion grants no Ready or process launch. Required unsupported hooks,
  policy references and native settings refuse before mutation. OpenCode effort
  remains unsupported. Existing published v0.1.0 is unchanged.

## v0.1.0

- Consolidated provider placement into `adapters/layout/plan`. Parent layout queries are pure derived compatibility views, including explicit existing MCP mirrors and permissions. The generator owns only `plan-fields.json` and `PLAN-FIELDS.md`; older layout exports remain historical evidence. Unsupported transport/variant queries refuse instead of falling back to an unrelated shape. Artifact and golden fixture bytes remain unchanged.


The harness module contains the packages of eleven former Hollis Labs modules, moved in with their git history at their final homes, and a stdio process shim library with its `cairn-shim` executable, written in this module. The old modules' release tags were not carried over. The module requires `llm-core` v0.1.0 and `mesh` v0.1.0 and builds with Go 1.26.6. This first library release consolidates launch planting under `agentlaunch/planting` and managed workspace mutation under the workspace root and concrete materialize engine. Runtime adoption is separate from library availability.

### Known issues

- `TestSandbox_OutsideWorkspaceReadBlocked` (`sandbox`) is skipped unconditionally on Linux: the skip says the test needs additional bind configuration, and it has said so since the sandbox was first extracted, before this move. So the test suite does not verify that a sandboxed process on Linux cannot read a denied path outside its workspace. That is not known to be broken; it is not verified. On macOS the same test runs only when `~/.ssh` exists. The other Linux sandbox isolation tests run and pass in CI: network isolation, loopback, protected paths and the filesystem-allowlist parity tests.
- `adapters/wrapper`: the wrapper takes the end of a Codex JSON-RPC stdio turn from the session's typed terminal event only when the CLI adapter is named `codex`. The session layer emits that typed terminal only for an adapter with that name, so a compatible custom adapter that speaks the same protocol under another name does not get it, and the fix for the duplicated turn lifecycle does not cover it. Other runtimes keep taking their terminals from the legacy event stream.

### Changed

- Workspace preparation freezes explicit credential and trust inputs, preflights
  all groups under complete locks before mutation, and records leaf evidence
  through one durable receipt store. Retry evidence retains earlier recovery
  obligations; artifacts remain Partial and do not grant launch readiness.
- Legacy planting requires explicit workspace authority and uses the sole
  materialize engine. Side-writer and engine overrides are removed. Existing
  unsafe roots, nonempty unmanifested content and credential placeholders refuse
  before mutation. Pure provider projection remains available separately.
  The interim `workspace/plant`, `workspace/providerplant` and `workspace/bootdir`
  packages are removed. `agentlaunch/planting` owns the wrapper `Planter`,
  `PlantSpec`, `PlantResult`, `PlantHook`, `SharedPlanter` and `NoOpPlanter`
  contracts and provider projection/preparation adapters. The bootdir writer
  has no public replacement; callers submit explicit artifacts through the
  sole materialization route. Historical bootdir coverage stays test-only.
  Archived baselines and seeds remain unchanged; active routing deltas are
  documented in `workspace/README.md`.

- Pre-first-tag API break: wrapper `Config.MaterializationEngine` and
  `WithMaterializationEngine` are replaced by `ArtifactAuthorization` and
  `WithArtifactAuthorization`, using `agentlaunch.ArtifactAuthorizer` instead
  of `materialize.Engine`. Removed fields are `SharedPrepareOptions.Engine`,
  `ArtifactMaterializationRequest.Engine` and `.Now`, `plant.SharedPlanter.Engine`,
  `MaterializerOptions.DirMode` and `bootdir.Writer.AtomicWrite`, without a
  compatibility authority adapter.
- All five session start paths require explicit inactive/private artifact
  custody through `StartOptions.ArtifactRoot` and `ArtifactAuthorization`.
  Missing/invalid authority refuses before rendering or mutation. Verified
  callbacks expose detached results; structured preparation/start errors retain
  roots and obligations. Terminal and start-failure paths no longer delete
  engine-owned roots; custody-aware retirement remains deferred.

### Added

- Explicit Claude/Codex installed artifact planning and apply through the
  concrete engine, external receipts and exact path/key authority. Existing
  user directories remain untouched; unreadable or drifted owned documents
  refuse. Real Linux and Darwin installed mutation is currently Unsupported:
  no trusted metadata-attestation issuer is implemented. A data-only original
  context and private detached consumer/verifier are present; synthetic tests
  exercise their binding rules without granting native capability. Installed
  completion remains artifact-only, without launch readiness.
- Closed publication accounting and origin inspection retain original requests,
  receipt origins and prior obligations across retries. Recorded pins use the
  existing mutation-lock mapping and separate exclusive probe/shared use
  descriptors. Publication remains Unsupported without trusted metadata,
  isolation and custody evidence; opaque use reservations do not earn Ready.

- Pure provider document assembly under `workspace/render`, native leaf-key
  ownership metadata, intended offline snapshots, and separate generated
  exports for the authored provider plan-field table. Library launch adapters
  route artifacts through the workspace authority boundary and sole engine.
- Installed Codex encoding preserves literal strings and explicit parent MCP
  tables; installed instruction markers retain their format and use the resolved
  definition identifier.
- Pure owned-key merge of JSON and TOML documents under `workspace/keymerge`,
  with typed outcomes and notes and a comparison for drift checks. A declared key
  is written only where the caller owns it, every other key of the document found
  stands as found (JSON keeps its order and its raw bytes), and a declared key the
  caller does not own is never overwritten or added. Owning every declared leaf
  reproduces the archived installer's merge byte for byte.
- Requirement: `github.com/pelletier/go-toml/v2` v2.2.4, imported only by
  `workspace/keymerge`.

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

- Headless Codex rendering now requires a bound posture or explicit host native
  default instead of falling back to an unevidenced interactive policy.
- Pure rendering clamps overlay permissions, rejects case aliases and bounded
  unsafe paths, rebuilds provenance and file digests, and requires operator
  ownership annotations to match emitted leaves exactly.

- The new artifact contract requests owner-only permissions for installed native
  settings and configuration. This requested mode narrows the archived Claude
  settings mode of 0644. Existing-file replacement refuses unpreservable
  ownership/mode or unknown metadata; native installed mutation is Unsupported,
  so this release does not tighten existing operator files.

- `adapters/layout/gen` writes `adapters/layout/layout.json` and `adapters/layout/docs/LAYOUT.md`, the files' new locations relative to the module root (the generator's two path constants; it would otherwise have written `layout/layout.json` and `docs/LAYOUT.md` next to the module root).
- Code that pinned a sibling at a tag now builds against the sibling's source at its old repository's `main`: `runner` (was `go-providers` v0.26.0, `go-sandbox` v0.3.0, `go-llm-types` v0.3.0), `adapters/provider` (was `go-llm-contracts` v0.1.0, `go-permission` v0.1.0) and `sandbox` (was `go-safefs` v0.1.0).
- `agentkit`'s code, which pinned `go-materialize` v0.1.0, `go-permission` v0.1.0, `go-providers` v0.46.0, `go-runner` v0.8.2, `go-sandbox` v0.6.0 and `go-safefs` v0.1.0 at tags, builds against their packages in this module, and its llm-core requirements are `llm-core` v0.1.0.
- `go-agent-wrapper`'s code, which pinned `agentkit` v0.21.0 and `go-runtime-events` v0.2.1 and the other sibling modules at tags, builds against their packages in this module: `agentkit` v0.26.1, `go-runtime-events` v0.2.2, the rest at their old repositories' `main`; its llm-core requirements are `llm-core` v0.1.0.

### Fixed

- Materializing BootSpec and provider planting now validate explicit authority
  and cancellation before renderer/resolver callbacks, retain late apply
  validation, and close each resolved authority once. Standalone provider
  projection remains pure and does not require authority.

- `adapters/wrapper`: Codex JSON-RPC stdio turns consume the session's typed terminal event, preserving stop reasons and failure diagnostics while emitting exactly one turn lifecycle. Removed the duplicate empty lifecycle and restored the previously skipped native turn-order test.

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
