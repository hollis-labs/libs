# Harness

The Cairn library foundation for provider projections, launch preparation,
sessions and managed workspace artifacts. Import packages from
`github.com/hollis-labs/substrate/harness` with Go 1.26.6. The module pins
`llm-core` and `mesh` at v0.1.0; it needs no workspace or local replacement.

## Package boundaries

- `adapters/provider`, `adapters/registry` and `adapters/layout` define provider
  rendering and launch conventions. `adapters/layout/plan` owns the sole
  authored field table; parent layout queries are derived compatibility views.
- `agentlaunch` describes launches and artifact authority. `agentlaunch/planting`
  projects provider files and bindings and prepares them through the sole
  workspace materialization route. Pure `ProjectExecution` requires no authority.
- `adapters/wrapper` and `adapters/agentsessions` consume prepared launches;
  materializing start paths require explicit inactive/private artifact authority.
- `workspace` plans explicit resources and inputs, acquires the complete lock
  union and preflights all groups before mutation. The concrete
  `workspace/materialize` engine writes managed artifacts; credentials,
  repositories and trust use typed handlers and one external receipt store.
- `workspace/render`, `workspace/keymerge` and `workspace/install` provide pure
  rendering, owned-key merge and installed-target planning.
- `shim` and `cmd/cairn-shim` provide a private authenticated stdio process shim.

See [workspace/README.md](workspace/README.md) for authority, recovery and
archived-versus-active golden coverage, [CHANGELOG.md](CHANGELOG.md) for API
breaks, and the [migration table](../docs/migration.md) for old import paths.
The interim `workspace/plant`, `workspace/providerplant` and `workspace/bootdir`
packages have been removed. Launch adapters live in `agentlaunch/planting`;
the old bootdir writer is retained only in historical golden tests.

## Supported scope and adoption requirements

This is a library foundation, not approval to modify a live operator home or
start a runtime. Callers provide explicit authority, physical observations,
canonical locks and a protected receipt store outside the published tree.
Missing, stale or ambiguous authority refuses before mutation. Existing
operator directories and unrelated document keys remain user-owned.

Real Linux and Darwin installed apply is **Unsupported**: complete metadata
visibility and preservation lack a trusted native attestation issuer. The
data-only original context and private consumer are implemented; private
synthetic positive tests do not establish native support. Installed OpenCode
and Antigravity are Unsupported. Unreadable installed documents refuse.

Publication likewise requires trusted metadata, custody and isolation evidence
that this release does not manufacture. Artifact completion is not launch
readiness. Opaque shared pin reservations do not earn Ready; matched durable
real-shim adoption, fresh validation and observed caller release remain
adoption requirements. Recovery and retirement retain uncertain obligations:
there is no automatic replay, compensation, deletion or complete-descendant
absence claim. Public deferred Recover/Retire operations remain Unsupported.

Archived renderer/artifact fixtures, offline active-routing coverage and
private synthetic accounting tests describe their own scope. They do not
validate live catalogs, model CLIs, native installed mutation, runtime
isolation or operator retirement. Known sandbox and custom Codex-adapter
limitations are retained in [CHANGELOG.md](CHANGELOG.md).

## Checks

From the repository root, run `scripts/check harness` for formatting, vet, build
and tests, or `scripts/check -race harness` for the race detector. Installed
provider tests require explicit opt-in. The live catalog parity test reads an
existing catalog when present; an absent catalog skips that separate check.
Library checks do not activate a provider or grant runtime capability.

MIT. See [LICENSE](LICENSE).
