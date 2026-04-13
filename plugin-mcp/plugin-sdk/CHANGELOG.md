# Changelog

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
