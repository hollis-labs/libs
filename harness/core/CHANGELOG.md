# Changelog

All notable changes to the `harness` module are documented here (the module was `harness` in the substrate repository). The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`harness/core/vX.Y.Z`. Repository-level changes (tooling, CI, docs) are in the root
`CHANGELOG.md`.

## [Unreleased]

### Added

- Catalog: `resource.state_changed` (payload `resource.state_changed@1`) and
  `deploy.state_changed` (`deploy.state_changed@1`), the one lifecycle vocabulary
  for every app. Resource and service `to`/`from` are `stopped`, `starting`,
  `running`, `deploying`, `degraded`, `wedged`, `failed` or `removed`;
  `restarted` and `recovered` are carried in `reason`, not as states. Deploys go
  `started` then `succeeded`, `failed` or `rolled_back`. Hosts keep using
  `fleet.node.*`; a wedged resource also raises `alert.raised`. New mapping.md
  section "Lifecycle" and `resource.` added to the Torque, Hadron, Nanite and
  Fragments Engine entries of `producers.json`.

## v0.14.0 — 2026-10-20

### Added

- `adapters/runtimeevents/ingest` (CW-20261020-0009): the contract for publishing
  events to the hub with `POST /events`. Limits (100 events, 1 MiB per request,
  64 KiB per payload), `ParseRequest`, `CheckItem` (the per-event rules in a fixed
  order, including the ingest event JSON Schema: the generic envelope plus a canonical
  `evt_` id and a required app, no new vocabulary restrictions; the per-kind payload
  schema is not validated), the per-event `Result` and `Response`, JSON Schemas for the request and
  the answer, and fixtures for other languages.
- `adapters/runtimeevents/catalog`: `producers.json` with `LoadProducers`,
  `ProducerFor` and `ProducerAllows`, the per-app list of kinds a producer may
  publish through the ingest. A test requires every non-deprecated catalog kind
  to have an owner, so kind ownership is reviewed with the kind.
- `sandbox.FSSpec.Hide` (yaml `hide`, CW-20261001-0263): paths a host-filesystem
  profile makes unreadable. Linux bwrap puts an empty tmpfs over an existing
  directory and a read-only `/dev/null` bind over any other existing path,
  after the protect binds, so it also covers a protected directory or a file
  inside one. A missing path is skipped; a relative path, `/`, a path through a
  symlink the child could re-point, and any use outside `HostFilesystem` are
  refused. macOS treats it as a deny (not claimed as a capability: unverified).
  New capability `CapHidePaths` (Linux bwrap) and
  `ResolvedAccessPolicy.WithHidden` (existing paths become denies).
- `agentsessions.StartOptions.HiddenPaths`, merged like `ProtectedPaths`; the
  launch fails with `ErrHiddenPathsUnsupported` where it cannot be enforced.

## v0.13.0 — 2026-10-16

### Changed

- `eventstage`: the Sanitize adapters recognise a retention tombstone
  (`payload_digest` set, mesh/core v0.6.0 and runtimeevents `Tombstoned`): they
  return the note `tombstoned`, never call the sanitizer and never set
  `class.sanitized`, so a tombstone is not flipped to sanitized and the hub
  backstop does not read it as a producer that skipped its choke point
  (CW-20261016-0213). A digest is not a way past the sanitizer: an event that sets
  `payload_digest` and still has a payload fails closed with the class
  `tombstone_with_payload` (payload replaced, digest cleared).

## v0.12.0 — 2026-10-16

### Added

- `eventstage`: the ordered stage chain for the event stream (contract D4
  rev 2, CW-20261016-0145). `Stage[E]{Name(); Apply(ctx, *E) (Outcome, error)}`
  mutates in place; `Chain[E]` runs Stamp, Sanitize, Classify in a fixed order,
  contains panics, keeps only a value-free failure class in `Report.Err` for every stage, fails Sanitize closed (payload replaced by
  `{"sanitize_error":"<class>"}`, `class.sanitized=false`, envelope still
  emitted) and runs Classify only after Sanitize completed. Sanitize adapters
  for `runtimeevents.Event` and `mesh.Event` call a `PayloadSanitizer` (the
  payload API of the shared redaction engine) and set `class.sanitized`,
  `redactions`, `sensitivity` and `pii`; `ModeVerifyAndScrub` is the hub
  backstop (a fail-closed event keeps `sanitized=false` and its error payload;
  a never-sanitized one is reported as `backstop_unsanitized`). Classify
  evaluates a versioned, self-testing rule document (`eventstage.rules/v1`)
  that can set only category, severity and labels, can match on
  `sanitized` (to label fail-closed events), fills an empty category from the
  event kind catalog and rejects rules whose category conflicts with it.
- `adapters/runtimeevents`: `Event.PayloadDigest` (`payload_digest`,
  optional) and `Event.Tombstoned` mark an event whose payload a retention
  pass removed (event retention D7, envelope tier). The value is `sha256:`
  plus 64 lowercase hex digits over the stored payload bytes, or `withheld`
  for PII. `Validate` and `JSONSchema` require the payload to be absent when
  it is set and check its format; events without it validate as before.
  `ToMesh` and `FromMesh` carry it to and from `mesh.Event.PayloadDigest`
  (a tombstoned payload is absent here and `null` on mesh). New conformance
  fixtures cover both directions (CW-20261016-0167).
- `adapters/runtimeevents/catalog`: kind `agent.subagent_stop` (activity,
  new) with payload schema `subagent_stop@1` (`subagent_id`, `outcome`
  required); `parent_id` is the matching `agent.subagent_spawn` event.
  `KindAgentSubagentStop` constant.
- `adapters/runtimeevents/catalog`: kinds `session.usage` (activity,
  `session_usage@1`: cumulative token totals at turn end and session end; an
  observation, never cost) and `session.health_changed` (state,
  `session_health_changed@1`: healthy, degraded, unhealthy or unknown, emitted
  only on change).
- `Actor` documents the `device` kind for machine principals (mesh/core
  `ActorDevice`, next mesh/core minor).

### Changed

- Requires `mesh/core` v0.6.0 (for `Event.PayloadDigest`).
- Requires `github.com/hollis-labs/libs/security/redact`.

### Fixed

- `adapters/acp.NewDiagnostic` redacted only keyed pairs and headers, and it bounded the text to 4096 bytes *before* redacting. Bare `Bearer` tokens, provider key shapes and URL userinfo passed through, and a credential straddling the bound was cut instead of matched. It now redacts the whole text with `security/redact` and then bounds the result (on a rune boundary). The auth scheme word (`Bearer`) is kept; only the credential is replaced.

## v0.11.0 — 2026-10-16

### Added

- `adapters/runtimeevents` envelope v1.1, additive (`SchemaVersion` stays "1"):
  optional `Subject`, `Generation`, `RecordedAt`, `Cursor`, `ContentType`,
  `Truncated` and the `Scope`, `Corr`, `Actor` and `Class` blocks.
  `Event.Validate` and `SessionBound` encode the required fields, and
  `JSONSchema` (draft 2020-12) mirrors them. `ToMesh` and `FromMesh` project
  losslessly to and from `mesh.Event`. Conformance fixtures are in
  `adapters/runtimeevents/testdata/conformance`, and the field table is in
  `adapters/runtimeevents/projection.md` (event stream contract C1,
  CW-20261016-0129).

- `adapters/runtimeevents/catalog`: the event kind registry (`catalog.json`,
  106 kinds with category, status, successor and payload schema id), 15
  minimum payload schemas `urn:hollis-labs:events:<name>@1`, generated Go and
  TypeScript constants, and `mapping.md`, which maps the kinds every producer
  emits today onto the catalog (event stream contract C2, CW-20261016-0130).

- `catalog` self-report contract: the `progress_detail` setting
  (`EffectiveProgressDetail`, `ProgressDetail.Allows`), the input and output
  schemas for the non-parking `torque_task_progress` and `torque_task_data_emit`
  calls (`ToolSchema`), and `selfreport.md` (event stream contract C4,
  CW-20261016-0132). `progress.checkpoint@1` now requires `effective_level`
  and records `detail_source`.

- `adapters/runtimeevents/scope.md`: the `scope` dimension vocabulary, the
  source of truth for each field and the data gaps producers must close first
  (event stream contract C3, CW-20261016-0131). `catalog/mapping.md` reserves
  `stream.*` for transport control signals such as `stream.gap`, which are
  never registered as kinds, persisted or relayed.

### Changed

- `runtimeevents.Event.SessionID` is now omitted from JSON when empty. Events
  that are not tied to a session carry `Subject` instead. Producers that
  always set a session ID emit the same bytes as before.
- Requires `mesh/core` v0.5.0.

## v0.10.0

### Changed (breaking)

- Module path is now `github.com/hollis-labs/libs/harness/core` (was `github.com/hollis-labs/substrate/harness`); the substrate path and its `harness/vX.Y.Z` tags no longer exist. Tags are `harness/core/vX.Y.Z`. Entries below keep the former package names.
- The harness is a layer of its own, strictly below the agent: this module no longer depends on the agent module, and the agent depends on it.
- `interception/permission` (with `pathgrants` and `summary`) moved to `github.com/hollis-labs/libs/security/policy/permission` so that the agent and the harness share one permission contract. Import it from there.
- `agentcontext` (with `resolvers` and `skills`) moved here from the agent module and is now `github.com/hollis-labs/libs/harness/core/agentcontext`. `agentcomposition` and `agentlaunch/contexthook` stay in this module and now import it from here.
- Requires `llm/core` v0.2.0, `mesh/core` v0.4.0 and `security/policy` v0.1.0 (were the substrate `llm-core`, `mesh` and `agent` modules).

### Added

- Observed Linux frozen payload isolation with protected control/store mount,
  proc, inherited descriptor, hard-link and mapping exclusions. Guarded capture
  requires a separate current host admission port, finite policy and bounded
  pre-ingestion redaction; absent or stale support refuses. No production host,
  owner-completion or deletion issuer is supplied.
- Selective captured-file restore with expected-current presence/SHA256,
  complete selection preflight and the same actual host writer fence through
  native replacement and durable accounting. Partial effects retain originals,
  pins, evidence and uncertainty; recorded operations never automatically retry.
  No live adoption, full-metadata restore or GC authority is added.
- Typed consumption of independent host completion/deletion proofs through
  held admission, without a public grant factory. Completion pins and durable
  uncertainty barriers survive failed accounting or host closure; KEEP,
  outstanding pins and unknown journals still dominate collection. Genuine
  production proof issuance remains a separate host integration.

## v0.9.0

### Added

- `clone` repository attachment mode (`workspace/repositories`): an independent
  private repository built copy on write (Linux FICLONE `reflink`, macOS
  `clonefile`) from a source whose HEAD is the pinned base with no tracked
  change. Capability is probed on the actual source/destination pair before any
  mutation; required copy on write without a method is `Unsupported`, and
  preferred copy on write falls back to a worktree only under a separate
  explicit authorization on the shared Git metadata root. Receipts record the
  requested mode, actual mode, method, fallback reason and fallback authority.
  A failure after construction starts is `Partial` and retained; no plain copy
  is ever substituted. Removing a clone is unsupported and retains it.
- Separately authorized `repository_mergeback` effect: a fast-forward of one
  source branch to a clone's head through a checked local fetch and an exact
  old-value ref update. It refuses checked-out or moved targets and never
  forces.
- Real copy-on-write construction is unmeasured: this repository's checks run on
  ext4, where the probe answers `none`/`reflink_unsupported`, and exercise the
  positive path only with a test-only `fixture_copy` cloner whose receipts say so.

## v0.8.0

### Added

- Per-root age/count retention overrides with whole-set KEEP and dominant
  journal/export/fork pins. Guarded GC records intent before exact-reference CAS
  deletion, preserves run-budget tombstones and retains interrupted journals.
- On-demand and cancellable finite host GC scheduling with opaque deletion
  authority and redacted outcome recording. Private disposable-fixture controls
  exercise object reclamation and pinned shared-object survival; no production
  isolation/deletion/completion issuer or live cleanup is supplied.

## v0.7.0

### Added

- Defensive target plans derived from effective write grants and host bindings,
  mandatory credential/Git exclusions, descriptor-relative private ingestion
  and finite capture/storage/run admission before reading candidate bytes.
- Durable set accounting and sanitized outcome manifests, held multi-root read
  leases and journal/export/fork pins. KEEP is the default; pins and uncertain
  reservations prevent age/count eligibility. Closing a read lease retains its
  pin; explicit owner completion requires an issued durable completion receipt.
- Receipt-bound snapshot event coordination with independent current host
  admission, atomic authority/idempotency persistence and honest coalesced source
  intervals. Failed captures without a retained receipt emit no taken event;
  persistence uncertainty retains obligations.
- Bounded pinned captured-object reads and separate new-root materialization
  through `ForkSnapshot` and the sole workspace `ApplyTree` engine. Source pins
  remain retained and full partial/error results survive failures. This is not
  COW cloning, Git-worktree fallback, repository attachment or full filesystem
  metadata preservation; those capabilities are not earned by materialization.

### Changed

- Pin published `mesh` v0.3.0 for the versioned snapshot schema and raise the Go
  floor to 1.26.9. Legacy raw ShadowGit behavior and nil-disabled snapshot ports
  remain unchanged; protected production capture is Unsupported until a trusted
  enforced-isolation/custody producer exists. No live adoption is included.

## v0.6.0

### Added

- Preserve the historical filesystem snapshot package and its Git history at
  `workspace/snapshot`, including ShadowGit tests and benchmarks.
  `workspace.Ports.Snapshots` accepts its unchanged capture/diff/preview/restore
  interface. Nil disables capture; materialization never invokes it automatically.
  Capture policy, finite budgets, secrecy, custody, pin-aware retention and guarded
  restore remain host integration requirements. Existing `sandbox/snapshot` stays
  available for compatibility.

## v0.5.0

### Added

- `agentsessions.AttachOptions.OnSnapshot` reports the oldest retained byte
  offset and exclusive head captured atomically with replay and live subscription.
  The callback runs outside locks before stream bytes; refusal releases the
  subscriber and attachment bookkeeping without writing bytes. Existing callers
  retain their replay behavior. The snapshot describes the subscription boundary,
  not subsequent ring movement or live subscriber drops.

## v0.4.0

### Fixed

- Runner SIGKILL escalation test waits for child output proving SIGTERM trap
  installation, including a delayed-installation fixture, instead of sleeping.

- Runner test fixtures build on Windows: isolate POSIX resource-limit reporting
  and CPU-signal tests while retaining zero-limit and unsupported-limit coverage.

### Added

- Import the historical `go-hooks` implementation with its Git history into
  `interception/hooks`, including the command runner and conformance fixtures.
  Existing interception policy, filter and permission APIs are unchanged.
- Publish migration instructions for the root, `cmdhook` and `conformance`
  imports. The standalone `plugin-hooks` catalog engine remains separate.
  Host-owned execution, required failure modes and known native payload
  normalization gaps remain explicit; no boot hook execution is added.

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
