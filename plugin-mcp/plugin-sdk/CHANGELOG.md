# Changelog

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
