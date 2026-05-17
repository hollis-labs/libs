# Changelog

All notable changes to go-apppaths are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.1.0 — 2026-05-17

First release. The `paths` package — the path/layout substrate every per-app
migration depends on (torque task CW-20260517-0059).

### Added

- **`Resolve(appName, ...Option) (Layout, error)`** — functional-options
  constructor returning an immutable `Layout`. Resolve once at startup and
  inject downward; no global singleton, no lazy resolution.
- **`Layout`** — exposes the four base roots (`DataDir`, `StateDir`,
  `CacheDir`, `ConfigDir`), the resolved `MainDB`, the active `Workspace`,
  and `Describe()` for an `<app> path` introspection subcommand.
- **XDG layout on every OS** — `~/.local/share`, `~/.local/state`,
  `~/.cache`, `~/.config`, honoring `$XDG_*_HOME` when set. adrg/xdg's
  per-OS defaults (e.g. `~/Library` on macOS) are deliberately bypassed so
  apps keep one portable layout.
- **Precedence chain** — main database: `WithDBOverride` > `<APP>_DB_PATH`
  env var > active workspace's database > the `default` workspace. Active
  workspace: `WithWorkspace` > `<APP>_WORKSPACE` env var > persisted
  pointer > `default`. The `<APP>` env prefix is derived from the app name.
- **Workspace registry** — list (`Workspaces`), resolve (`ResolveWorkspace`),
  and select (`SelectWorkspace`) named workspaces; the active workspace is
  persisted as a small state-file pointer.
- **Options** — `WithWorkspace`, `WithDBOverride`, `WithProjectMode`
  (opt-in CWD-local roots), `WithLegacyNames`, `WithWarnWriter`,
  `WithoutMaterialize`.
- **Legacy adoption** — `WithLegacyNames(...)` migrates a prior app's base
  roots on resolve: idempotent move-if-target-absent, with a loud warning
  and no clobber when both the legacy and current directory exist.
- **Directory materialization** — `Resolve` `MkdirAll`s the resolved roots
  (0o755) by default; `WithoutMaterialize` opts out.

### Notes

- Scope is paths only — go-apppaths never opens the database and never
  parses application config files. Composition: apppaths → path →
  sqlite-open layer.
- Dependencies: standard library + `github.com/adrg/xdg`.
