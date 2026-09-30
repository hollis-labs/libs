# Changelog

All notable changes to go-materialize are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Changed

- Raised the module's `go` directive to `1.26.6` (Go floor across the portfolio); CI now uses `go-version-file: go.mod`.

### Added

- Extracted `artifact` and `materialize` packages from `agentkit` v0.6.1
  (CW-20260918-0036): value types, `DefaultEngine.Apply` (atomic staged
  create with symlink/traversal defense), `Reconcile`/`Refresh`, and
  `MergeDocument` (JSON/TOML managed-key merge).
- `materialize.ExistingTargetAllowEmpty`: a new `Request.ExistingTarget`
  policy that lets `Apply(OperationCreate, ...)` publish into a
  pre-existing, empty target directory instead of always refusing —
  needed by callers (e.g. folio) that tolerate an empty target but not a
  populated one. `Request.ExistingTarget` was previously a declared but
  dead field in agentkit's copy.

### Changed

- Manifest path renamed from the agentkit-branded
  `.agentkit/materialize-manifest.json` to `.materialize/manifest.json`
  (breaking for existing agentkit consumers on upgrade — see the
  consuming repos' own changelogs).
- Internal stage/tmp file name prefixes (`.agentkit-stage-`,
  `.agentkit-tmp-`) renamed to `.materialize-stage-`/`.materialize-tmp-`.
- `Manifest.SchemaVersion` renamed from `agentkit.materialize.v1` to
  `materialize.v1`.
- `MergeDocument` wraps the underlying JSON decode error as well as
  `ErrMalformedDocument` (`%w` for both), so `errors.As` reaches the
  `*json.SyntaxError`; `errors.Is(err, ErrMalformedDocument)` is unchanged.
- The manifest directory (`.materialize/`) is created `0o750` instead of
  `0o755`.
- Reconcile/Refresh planning checks the context between entries, so a
  cancelled caller stops before reading the rest of the target.

### Fixed

- `golangci-lint` is clean against the repo's own config for the first
  time since the extraction (shadowed `err`s renamed, test file and
  directory modes tightened, reasoned `//nolint:gosec` where a read path
  is the caller's by design). CI had failed at the lint step on every run.
