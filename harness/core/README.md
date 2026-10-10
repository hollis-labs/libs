# Harness

The Cairn library foundation for provider projections, launch preparation,
sessions and managed workspace artifacts. Import packages from
`github.com/hollis-labs/substrate/harness` with Go 1.26.9. The module pins
`llm-core` at v0.1.0 and `mesh` at v0.3.0; it needs no workspace or local replacement.

## Package boundaries

- `boot` provides pure planning and workspace-backed preparation for explicit
  resolved dispatches. Its detached process description and complete partial
  apply result grant no launch readiness. See [boot/README.md](boot/README.md).
- `cmd/cairn` accepts `boot --resolved` inputs; `--plan` is pure. Default
  preparation preserves the missing-host-port refusal and complete result.
  Decoded data never constructs authority. The older Cairn module and installed
  command remain separate; no live cutover is included.
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
- `workspace/snapshot` preserves the historical ShadowGit filesystem mechanism.
  Hosts inject it through optional `workspace.Ports.Snapshots`; materialization
  never captures automatically. See [snapshot integration limits](workspace/snapshot/README.md).
  The guarded capture kernel adds explicit target/exclusion plans, finite
  pre-capture budgets and durable complete-set pins. Its production constructor
  remains Unsupported until actual enforced isolation and custody are available;
  private native fixtures do not establish that support. See
  [scope and confidential ingestion](workspace/snapshot/docs/target-policy-and-secrecy.md).
- `workspace.RecordSnapshotEvent` projects only a verified retained manifest
  while its journal read lease stays held. An independent host supplies current
  authority, authenticated source replay and atomic event persistence. Missing
  ports refuse; uncertain persistence retains pins and reconciliation obligations.
  No automatic capture, fabricated detached tool history or host issuer is supplied.
- `workspace/render`, `workspace/keymerge` and `workspace/install` provide pure
  rendering, owned-key merge and installed-target planning.
- `interception/hooks` preserves the eleven-event hooks contract, pure layer
  resolution, an explicit subprocess runner and conformance fixtures. Hosts
  own engine wiring and execution authority. See [its migration guide](interception/hooks/MIGRATION.md).
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
