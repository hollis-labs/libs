# Changelog

## v0.6.1 — 2026-10-02

- HTTP requests now carry optional `RawPath` and `RawQuery` fields so hosts can
  preserve escaped separators, repeated query values and empty parameters across
  canonical `http/handle` dispatch. The subprocess protocol remains version 1.

## Unreleased

### Changed

- **Breaking:** manifest-v2 execution now requires protocol 2, `server`
  (`runtime`, inclusive `engines`, bundled `entry`) and an exact SHA-256 artifact
  inventory. Removed `entrypoint` and its Go type; protocol-1 manifests fail.
  Host/runtime compatibility uses numeric SemVer including prereleases, with no
  local/0.x bypass. Go/TS Serve remains protocol 1 pending its separate update.
  See [manifest layout, digest and enforcement](docs/manifest.md).


- **Breaking:** browser contribution registry v2 replaces the legacy registry protocol-1 shape.
  Entries carry owner generation and representation-specific data, component,
  or public handler references; hosts explicitly opt into kind/region schemas.
  Bundle versions are SHA-256 digests with named runtime admission. Only registry_version:2 is emitted; legacy protocol-only/dual-key documents
  are refused by name, with no fallback; applications remain on their released SDK until separate adoption.

- Raised the module's `go` directive to `1.26.6` (Go floor across the portfolio); CI now uses `go-version-file: go.mod`.

### Added

- Node-only `@hollis-labs/plugin-sdk/build` subpath for strict manifest-v2
  author declarations, file collection, SHA-256 inventory/tree digest, canonical
  manifest writing and immutable staged-tree verification. Shared Go/Node
  fixture includes binary/UTF-8/CRLF bytes and executable modes. Authoring guide
  explains schema-first scaffolds and the future host reload boundary.


- Manifest UI isolation preferences and catalog-shaped hook declarations,
  `TreeDigest` with a specified path/hash/executable-byte algorithm,
  `CheckCompatibility` with fail-closed runtime/host bounds, and `VerifyBundle`
  for immutable staged trees. Verification rejects links, special files,
  inventory/content/mode mismatches and a changed reviewed manifest. Hosts own
  trust, runtime permission flags, snapshot publication and execution policy.


- `registry.Catalog` publishes atomic, epoch-bound revisions and refuses owner
  replacement or withdrawal before explicit revocation. `registry.Scope` fences
  calls, cancels admitted work, and attempts reverse-order idempotent disposal.
  Optional contribution refusals remain visible; required refusals abort planning.
  Host-declared status is required per entry; inactive/unknown states remain listed
  and never execute, with unknown-state diagnostics and one authoritative refusal
  source in refusals[].
  Registry-v2 shared fixtures and a [wire/lifecycle contract](docs/protocol/registry-v2.md).

- Optional manifest tool `annotations` using MCP names: `title`,
  `readOnlyHint`, `destructiveHint`, `idempotentHint` and `openWorldHint`,
  preserving explicit false. The only cross-field refusal is read-only plus
  destructive; idempotent reads and destructive tools are valid, and effects
  remain host-defined. Hints never grant authority. Historical golden
  definitions for context_pin, context_unpin and reminder_set are pinned to
  Nanite source commit `2de304e3d1cebe8d875f7806c03ec0eae8f6b8fe`.
  The historical annotations addition preserved existing execution envelopes;
  the new execution contract above now replaces them. Manifests using annotations require
  the SDK release carrying this field: older strict hosts refuse them with
  `manifest: unknown field "annotations"` (documented fixture in
  `manifest/testdata/annotations/older-strict-host-error.txt`). Rollout order:
  SDK release, Nanite SDK bump, then updated nanite-plugins manifests.
  No schema, subprocess protocol or package version changes; host MCP
  registration is separate.

- `manifest`: shared schema version 2 declarations with dotted IDs, subprocess
  server entries, host/engine ranges and opaque host extensions, config fields and
  host-resolved secrets, capability requests and manifest-authoritative tools.
  Includes validation and a dependency-free JSON codec (valid YAML). Legacy
  host dialects, builtin runtimes and release signature fields are refused.
  Effects remain host-defined declarations, never SDK permission grants.

- **Identity passthrough plumbing** — an optional, opaque `Identity
  json.RawMessage` field on `InitParams`, `CommandExecParams`,
  `EventHandleParams`, `MCPCallRequest`, and `HTTPRequest` (and on the
  SDK-friendly `CommandRequest`/`EventRequest`), plus a new
  `subprocess.IdentityAware` capability interface. A host that has
  already verified a caller (e.g. via `go-mcp/auth`) can plumb that
  verified identity through to a plugin subprocess; plugin-sdk never
  picks a token scheme or validates anything — it is a courier, not an
  authority. `Serve` calls `IdentityAware.Identity` once after a
  successful `plugin/init` when `InitParams.Identity` is set, and again
  before any `Command`/`EventHandle`/`MCPCallTool`/`HTTPHandle`
  dispatch whose request carries its own `Identity`; a plugin that
  doesn't implement `IdentityAware` is unaffected, and one that doesn't
  receive an `Identity` value is never called. (CW-20260918-0043)

### Notes

- Known gap, not fixed here: `subprocess.Serve`'s dispatch loop has no
  case for `mcp/list_tools` (`MethodListTools`) and there is no
  capability interface for it, so a plugin cannot answer it even though
  the method name is declared in `protocol.go`. Filed as CW-20260918-0048.

## v0.5.0 — 2026-09-18

### Added

- **`subprocess.CapabilityRequest`** — a declaration mechanism with an open
  vocabulary, so a plugin can say what ambient access it needs and a host can
  decide what to allow. `Name` is an open string in the host's vocabulary and
  `Metadata` is opaque JSON, following the precedent the registry contract sets
  for contribution kinds. The SDK defines no capability names.
- **`subprocess.InitParams.Granted`** — the capability names the host allowed,
  travelling back on the existing `plugin/init` handshake, plus
  `InitParams.HasCapability` to read it. A plugin can discover what it actually
  received instead of assuming it got what it asked for.
- **`subprocesstest.WithGranted`** — seeds the granted list on the harness's
  mocked `InitParams`. The harness grants nothing by default, so a plugin's
  degraded path is the one its tests exercise unless a test says otherwise.

### Notes

- This is a declaration mechanism and nothing more. The SDK grants nothing,
  enforces nothing and names no capability, because a host's trust and
  isolation model does not live in this module and a capability name here would
  be one host's vocabulary inherited by every other. A granted list is a
  statement, not a boundary — `docs/security-model.md` says so in the table and
  in the seams.
- An absent capability is not a refusal. A host that predates the mechanism
  sends no `granted`, and a host that grants nothing sends an empty one; the
  wire does not distinguish them, and `HasCapability` does not pretend to.
  Plugins degrade on absence rather than refusing to load.

### Compatibility

**A minor bump, not a patch.** `CapabilityRequest`, `InitParams.Granted`,
`InitParams.HasCapability` and `subprocesstest.WithGranted` are new exported
API, and a patch release in this repo is documentation, examples and internal
hardening only (see the Status section of `README.md`).

Fully backward compatible with v0.4.0 and additive in both directions, asserted
by tests rather than by argument: `TestInitParamsForwardCompatNoGranted` (a
newer plugin against a host that sends no `granted`),
`TestInitParamsBackCompatOlderPluginIgnoresGranted` (a v0.4.0-shaped plugin
decoding init params from a host that does) and
`TestInitParamsGrantedOmittedWhenUnused` (a host that grants nothing emits the
same payload it emitted before the field existed). `ProtocolVersion` is
unchanged at 1.

## v0.4.0 — 2026-09-16

### Added

- **`registry`** — the Go view of the plugin registry wire contract: the
  `Response` a host serves so a browser can find, load and resolve the UI its
  plugins ship, the `Protocol` constant pinned at 1, and `Validate`. Types,
  the constant and validation only: a host owns its own endpoint and its own
  caching.
- **`ts/`** — a TypeScript companion workspace, so the registry wire contract
  is defined once with a Go view and a TypeScript view rather than
  hand-matched across two repositories.
- **`@hollis-labs/plugin-registry`** (`ts/packages/plugin-registry`) — the
  browser half. `createPluginRegistry` dynamic-imports each plugin's ES
  module, resolves the named exports the registry names, isolates and
  attributes load failures per plugin, and notifies subscribers. It works with
  no configuration; `adopt`, `reserved`, `importModule`, `stylesheets` and
  `onDiagnostic` are overrides. The core entry point has no dependencies.
- **`@hollis-labs/plugin-registry/react`** — the React adapter, behind an
  optional peer dependency: `createReactPluginRegistry`, `reactAdopt`, and
  `useSyncExternalStore` hooks.

### Notes

- The registry contract treats a contribution *kind* as an open string and its
  metadata as opaque JSON. It deliberately does not build on `UIComponentType`
  in `plugin.go`, which is one host's taxonomy that already lives in this
  module.
- The two views are authored rather than generated, and there is no shared
  golden fixture. Both halves pin the protocol number and round-trip their own
  types, so a host and a loader at different versions disagree about the
  protocol rather than about a field — the same answer
  `TestProtocolVersionLockedAt1` already gives for the subprocess wire.

### Compatibility

Fully backward compatible with v0.3.1. `registry` and `ts/` are new
packages; no changes to existing exported symbols or to the
`subprocess` wire protocol (`ProtocolVersion = 1` unchanged).

## v0.3.1 — 2026-05-10

### Changed

- **README rewritten** for a public, host-neutral audience: status banner,
  godoc badge, install snippet, runnable quickstart, layout reference,
  and pointer to `examples/hello/`. No host-specific framing or
  internal-product references.
- **Top-level package documentation** moved to a dedicated `doc.go`
  giving an overview of the package layout for `pkg.go.dev`.
- **Inline godoc reframed** in `plugin.go`, `envelope.go`, `errors.go`,
  `plugin_test.go`, `subprocess/types.go`, `examples/hello/`, and
  `subprocess/subprocesstest/harness.go` — host-neutral language
  throughout, removing references to internal phase / track names and
  product-specific package paths. No exported-symbol changes.

### Added

- **`PLUGIN_SDK_JSON_ROUNDTRIP`** — host-neutral environment variable
  name for opting into JSON-roundtrip mode in the
  `subprocess/subprocesstest` harness. Both env names are honored;
  set either to a truthy value to enable.
- **`.gitignore`** with Go defaults plus explicit deny entries for
  agent-tool artifacts (`.agentrc/`, `.claude/`, `CLAUDE.md`,
  `AGENTS.md`, `.lefthook.yml`, etc.) and `.env*` files.
- **`doc.go`** at the package root for `pkg.go.dev` rendering.

### Deprecated

- **`NANITE_PLUGIN_SDK_JSON_ROUNDTRIP`** env var — still honored for
  backward compatibility, will be removed in a future release. Prefer
  `PLUGIN_SDK_JSON_ROUNDTRIP`.

### Compatibility

Fully backward compatible with v0.3.0. No exported-symbol or wire-
protocol changes; the `ProtocolVersion = 1` constant is unchanged. The
deprecated env var continues to work; set the new name to silence the
deprecation in your CI configuration.

## v0.3.0 — 2026-04-13

### Added

- `MCPHandler` plugin-side capability interface — plugins implement `MCPCallTool(ctx, MCPCallRequest) (MCPCallResult, error)` to serve `mcp/call_tool` requests. Tool registration remains declarative via `plugin.yaml`; the handler services runtime invocations only.
- `HTTPHandler` plugin-side capability interface — plugins implement `HTTPHandle(ctx, HTTPRequest) (HTTPResponse, error)` to service `http/handle` requests for routes registered via `plugin.yaml`. Streaming is not supported on this path.
- `subprocess.Serve` dispatches `mcp/call_tool`, `http/handle`, and `plugin/migrate` methods. Previously the method constants and wire types existed (v0.2.0) but no dispatch path routed them to a handler — plugins implementing these could not be invoked.
- `Migrator` is now dispatchable. Plugins that implement `Migrate(ctx, from, to string) error` receive `plugin/migrate` calls before `plugin/load` when the installed manifest version differs from the declared version.

### Compatibility

- Fully backward compatible with v0.2.0. Plugins that do not implement the new interfaces continue to return `ErrCodeMethodNotFound` for the corresponding methods.

## v0.2.0 — 2026-04-13

### Breaking

- `LoadResult` no longer carries `Commands`, `Slots`, `Components`, `Keybindings`, `ConfigSchema`, `EventSubscriptions`, `CRUDResources`, or `Dependencies`. These registrations are now declared in `plugin.yaml` and applied by the host at load time. Plugins that populated these fields at runtime will have them silently ignored — migrate by moving declarations to the manifest.
- The wire-type structs that previously rode inside `LoadResult` (`CommandRegistration`, `CommandArg`, `ComponentRegistration`, `UISlotEntry`, `KeybindingDef`) have been removed. No in-tree consumers remained after the `LoadResult` shape change.
- `MethodCallTool` renamed to `MethodMCPCallTool` (wire string `mcp/call_tool` unchanged). This aligns with the new `MCP`-prefixed wire types.

### Added

- `LoadResult.SkippedRegistrations []SkippedRegistration` — informational list of yaml-declared registrations the plugin declined at load time. The host logs and proceeds; there is no yaml fallback.
- Method constants: `MethodMCPCallTool`, `MethodHTTPHandle`, `MethodMigrate`.
- Wire types: `MCPCallRequest`, `MCPCallResult`, `HTTPRequest`, `HTTPResponse`, `MigrateParams`, `MigrateResult`.
- `CommandExecResult.Envelopes` and `EventHandleResult.Envelopes` — envelope emission alongside command/event responses. `subprocess.Serve` now propagates `CommandResult.Envelopes` and `EventResult.Envelopes` into the wire payload (previously dropped on the floor).

### Migration

Plugins on v0.1.x that returned populated `LoadResult` fields from `Load()` must:

1. Move the declarations into `plugin.yaml` (the host already reads them from there).
2. If a registration needs to be conditionally disabled at runtime, emit a `SkippedRegistration` from `Load()` instead of omitting it.

## v0.1.2 — 2026-04-13

### Added

- `InitParams.DataDir` — persistent per-plugin data directory (populated by host, absolute path).
- `InitParams.CacheDir` — ephemeral per-plugin cache directory (populated by host, absolute path).
- `InitParams.LogLevel` — host-requested log level (`"debug"` | `"info"` | `"warn"` | `"error"`).
- `InitParams.ResolvedDataDir()` helper — returns `DataDir` or `ErrNoDataDir` when unset.
- `InitParams.ResolvedCacheDir()` helper — returns `CacheDir` or falls back to `os.TempDir()` when unset.
- `ErrNoDataDir` sentinel error.

### Compatibility

- Fully backward compatible with v0.1.1. Older hosts send zero-value strings for the new fields; v0.1.2 plugins running against a v0.1.1 host receive empty strings and can use the `Resolved*` helpers to handle the fallback.
