# Changelog

All notable changes to go-apppaths are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

Targets v0.2.0 (not yet tagged). Implements the 0700 permission default
(`factory_bootstrap_defaults.q35`, 2026-09-30).

### Changed

- **Behavior change: existing directories are retightened.** `Resolve` now
  forces `0700` onto the four base roots, the workspace directory and the
  workspace database's directory, and `0600` onto the `active_workspace`
  pointer, *including when they already exist* at the `0755`/`0644` v0.1.x
  created. The first `Resolve` after upgrading changes the modes of existing
  installs; anything relying on another local user reading these paths will
  stop working. This is a behavior change to existing paths rather than a new
  feature, which is why it is a minor bump (v0.2.0) and not a patch; pre-1.0
  semver permits it, and the path layout itself is unchanged.
- The database directory named by `WithDBOverride` or `<APP>_DB_PATH` is still
  only created (0o755), never chmodded: it may be an operator-chosen shared
  path.

### Security

- Closes the world-listable-by-default gap: v0.1.x created every root `0755`,
  exposing database and state contents to other local users. Tesseract carried
  its own `internal/fsperm` workaround for this; other consumers had none.
- Symlinked roots are not followed when tightening. A path the process may not
  chmod is left as found instead of failing `Resolve`. On Windows the modes are
  not enforced.

### Added

- Exported `DirMode` (`0o700`) and `FileMode` (`0o600`).
- `examples/ownerperms` runnable example and `ExampleDirMode`.

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
