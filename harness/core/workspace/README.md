# Workspace planning and inactive preparation

`Plan` freezes explicit resources, observations, rendered artifacts and host
effect inputs. `Materialize` acquires the complete canonical lock set, validates
fresh authority and disk evidence, and preflights every credential, repository and trust
group before the first receipt or mutation. It commits and verifies artifacts,
then applies credentials, repositories and finally trust through the same durable
`Ports.ReceiptStore`. The leaves receive only its narrow `effects.ReceiptSink`
view.

Callers supply empty effect headers; the planner binds version, operation ID and
input digest. Credential inputs include an explicitly captured provider home,
source-read grants and private inactive candidate custody. Trust inputs name an
explicit configuration root and stable final target. Missing inputs, unknown
or deferred effects and unsupported host capabilities
refuse before mutation. No default discovers a home or grants authority.

`Resources.RecoveryReceipts` carries trusted earlier-operation evidence and
obligations into a retry. The root checks identity and declared roots, preserves
obligations and evidence, and neither replays nor compensates earlier effects.
Use a new operation ID for changed inputs. Uncertain staging and failed durable
records retain affected roots and report Partial.

Repository requests supply observed source/common/base identities and explicit
ownership and authorization revisions. The planner freezes the concrete branch
and base without interpreting templates, binds requests one-to-one to `RepoSpec`,
and includes exact source/common/base keys in the complete lock union. Every lock
namespace lies outside those roots. Missing inputs and private checkout creation
refuse; user-owned writable attachments need a separate write grant, and readonly
attachments need explicit enforcement evidence. There is no ambient discovery.
A `clone` `RepoSpec` names `required` or `preferred` copy on write and binds to a
frozen clone request; a worktree fallback needs its own granted repository
effect on the common root. Construction is selected under the locks at apply
and recorded in the attachment evidence, which resume and recovery bind back to
the frozen request (see `repositories/README.md`). Merge-back is a separate
repository effect and is not run by `Materialize`.

`Receipt.RepositoryRequests` retains original operation-bound requests beside
versioned attachment evidence. `Receipt.RepositoryOrigins` preserves the trusted
originating receipt pins and repository members across aggregate retries. Admission
binds fresh members to their own receipt and inherited members to those preserved
origins before flattening or inspection; matching foreign inner headers cannot
substitute an originating operation or digest. This is trusted host evidence,
not cryptographic authentication. Resume inspection uses original headers with
fresh authority and complete locks. Completed attachments may have advanced
HEAD or dirty work and skip creation. Intent or interrupted evidence remains
Partial with retained attachment roots even when `Created` is false. Inspection
never resets, removes or replays an attachment; earlier recovery obligations
survive later successful attempts. Repository failures prevent subsequent trust.
Retirement and runtime adoption remain separate.

Successful preparation is artifact-only Partial with a pending launch
reservation. `ArtifactsComplete()` requires the root's earned completion seal;
serialization or editing a result cannot earn it. Preparation does not publish
current, retire roots or issue Ready.

## Legacy routing changes

Legacy planting now requires an explicit `agentlaunch.ArtifactAuthorizer` that
supplies a `TreeRequest`, host ports and durable receipt storage outside the
published tree. `ApplyTree` uses the same workspace authority, lock, manifest and
receipt boundary. There is one concrete materialize engine; the legacy engine
injection, directory-mode default and `bootdir.Writer.AtomicWrite` side writer
are removed. `OnWritten` observes completed metadata only.

Materializing `DefaultMaterializer.Populate`/`Replant` validate authority and
context before slot callbacks. `providerplant.PrepareExecution`/`Plant` validate
before provider resolution or projection. Each operation resolves authority once,
closes its resources on render failure, and hands that same authority to late
apply validation on success. Cancellation prevents further active callbacks.

`providerplant.ProjectExecution` remains pure and returns rendered artifacts
and spawn bindings without a materialization claim. `PrepareExecution` validates
those bindings before routing artifacts. The historical Codex `auth.json`
placeholder remains in pure projection; artifact-only routing refuses its
reserved destination. Explicit typed credential groups are required for links.

The archived baseline and seed files remain historical evidence. Active routing
comparisons name these deltas and continue comparing payload bytes, file modes,
omissions, bindings and ownership:

| Historical apply behavior | Active routing behavior |
| --- | --- |
| Root defaults of 0755 or 0750 | Explicit private 0700 root; an existing unsafe root refuses without chmod |
| Managed empty directory declared 0750 | That input refuses; the separately named active fixture explicitly declares 0755 |
| AtomicWrite callback can write bytes | Sole engine writes; metadata observer runs only after success |
| First reconcile synthesizes ownership for nonempty unmanifested content | Typed refusal; operator bytes and modes remain untouched |
| Empty root bootstraps a reconcile manifest | Physical Create commits its first real manifest; later refresh uses its actual generation |
| Owned drift is overwritten | Conflict is reported; drifted bytes and modes are preserved |
| Context collision can leave an earlier slot written | All names are checked before any mutation; successful context routing commits a real manifest |
| Codex credential placeholder can clobber a credential file | Reserved destination refuses before artifacts or receipts; pure projection and bindings remain covered |
| Session parents default to 0750 | Sole engine creates declared artifact parent directories at 0755 inside the private root |
| Session planter ignores an explicit file mode | Active routing honors the pure renderer's declared mode; defaults apply only to an undeclared mode |
| Session terminal/start failure deletes its boot root | Committed and partial roots remain with trusted evidence; retirement is deferred to S6 |
| Empty legacy generation | Frozen tree input digest names the committed generation |
| Literal desired entry lacks provenance | The adapter explicitly names its own desired entry; existing disk entries are never adopted |

The separately versioned plan and tree digest domains are
`workspace.plan.input.v2` and `workspace.tree.input.v2`. Their protocol tests pin
encoding and input binding; this is independent of the provider renderer corpus.

## Session authority and exported API break

Before the first harness tag, `adapters/wrapper.Config.MaterializationEngine`
and `agentlaunch.WithMaterializationEngine` are removed. Their replacements are
`ArtifactAuthorization` and `WithArtifactAuthorization`, both using
`agentlaunch.ArtifactAuthorizer`, rather than `materialize.Engine`. The removed fields are `agentlaunch.SharedPrepareOptions.Engine`,
`agentlaunch.ArtifactMaterializationRequest.Engine` and `.Now`,
`plant.SharedPlanter.Engine`, `agentlaunch.MaterializerOptions.DirMode` and
`bootdir.Writer.AtomicWrite`. Their replacements supply explicit authorization;
`bootdir.Writer.OnWritten` observes committed metadata only.
No compatibility adapter converts an engine or a path into authority.

`ArtifactAuthority` supplies explicit inactive/private custody attestations,
a `workspace.TreeRequest`, host ports and a resource-close function. Its exact
root, operation, grants, current observation window and existing private root
mode are checked before session rendering. All physical evidence and host
authority are checked again under the complete locks before mutation. Host ports
must respect cancellation; arbitrary blocking host implementations cannot be
made cancellable by this library.

All five `agentsessions` start paths (per-turn, PTY, streaming stdio, JSON-RPC
stdio and HTTP) use `StartOptions.ArtifactRoot` and `ArtifactAuthorization`.
Applicable automatic planting without valid authority returns typed Unsupported
before rendering, callbacks or filesystem mutation. Disabled planting, missing
provider support and empty file specifications remain no-ops. `BootDirRoot`
remains request metadata and grants no authority or automatic root selection.

Options and adapter clones change only after a verified artifact commit.
`OnArtifactPrepared` receives detached structured evidence and handles, followed
by `OnBootDirPlanted`; an observer cannot mutate the retained evidence.
`ArtifactPreparationError.Result` exposes failed/partial preparation and start
failure accounting. There is no terminal or failed-start removal of engine-owned
roots. The host retains the result for eventual custody-aware retirement; this
stage does not implement S6 retirement or make artifacts launch-ready.

Archived session cases are checked as pure historical projection and bindings,
without recreating their independent writer. Active session routing is checked
separately with explicit authority, real manifests and the named mode/retention
deltas above. Pure Codex credential placeholders remain covered; their active
artifact-only preparation is unsupported before mutation.
