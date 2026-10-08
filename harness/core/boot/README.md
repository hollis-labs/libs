# Resolved boot preparation

`boot` consumes a caller-resolved definition, policy, dispatch and composed
context. It does not read profiles, definitions, catalogs or ambient environment,
run hooks, start processes, or infer authority from a path.

```go
planned, err := boot.Plan(input, host)
if err != nil {
    // planned.Description() still reports supplied provenance and requests.
    return err
}
description := planned.Description()
_ = description // process instructions, not a launch capability

result, err := boot.Prepare(ctx, input, host)
// Always inspect result, including when err != nil.
apply := result.Apply
retained := apply.Retained
obligations := result.Obligations()
_ = retained
_ = obligations
```

`Plan` is pure and freezes its inputs. Its `Digest` is the underlying workspace
artifact/effect digest. `Description` returns detached values. `Prepare` projects
the process before mutation, then calls `workspace.Materialize` once and forwards
its complete result, including receipt, handles, diagnostics, retained roots and
obligations. An error supports `errors.As` to `*boot.Error`, with phase `plan`,
`project` or `materialize`, and `Unwrap` preserves underlying diagnostics.
Pre-materialization errors have no earned apply result; their description retains
the supplied requests and provenance. No failure triggers cleanup or rollback.

`ArtifactsComplete()` delegates to the workspace engine's earned artifact proof.
It can coexist with `Partial` and recovery or launch-reservation obligations. A
caller-built or modified receipt cannot mint that proof. There is no `Ready`
method. Publication, installed apply, runtime isolation, actual shim adoption and
process lifecycle remain separate contracts.

## Input and host boundaries

- `Input.Dispatch` requires provider, transport mode, model and effort for every
  dispatch. No catalog or definition value supplies a missing selection.
- `Definition.Policy` carries the resolved named permission binding, restrictions,
  approval and escalation references, and source provenance. The binding is
  compared with the existing permission table and explicit host ceiling. Unknown
  names, including `catalog-auto`, refuse. Approval/escalation references preserve
  their opaque URI and resolved SHA256; nonempty requirements currently refuse
  because boot implements no approval or escalation engine.
- `Input.Workspace` carries semantic directory/access requests and identity pins.
  Home, per-launch boot, session scratch, repository and CWD meanings stay distinct.
  `HostInputDTO.Resources` supplies separately authorized roots and grants, with
  explicit observations, ceilings, credentials, typed effect inputs, MCP endpoints
  and environment. Requests never become grants. Caller-supplied effect attestations
  in `Input.Workspace.EffectInputs` refuse; they belong to `HostInputDTO.Effects`.
- `ContextHook` is finished neutral data: selected plan fields, required/optional
  registrations, components, source pins and already composed bytes/packages.
  It is not a callback. Subagents and static/dynamic context use this same transport;
  the caller owns missing-input policy and composition. Required registrations
  that lack support or pins refuse rather than being hidden.
- Supplied native settings are mandatory intent. Claude's resolved settings retain
  exact `fullscreen` and explicit false booleans. A foreign provider shape, unknown
  TUI value, conflicting effort or supplied native permission mode refuses. Native
  permission settings never override the effective bound policy. Unknown required
  hooks refuse; optional hooks produce an omission diagnostic. Nothing is executed
  or installed by the boot call.
- All host environment entries, including `TMPDIR`, `GOTMPDIR` and `TEAM_*`, carry
  provenance. Runtime policy/discovery collisions refuse. No environment is read
  from the running process. Definitions, policy, dispatch, context, settings, hooks
  and host inputs retain their separate origins on errors and partial results.

`HostInputDTO` is serializable data, not an authority capability. Library callers
must supply `HostInputs.Ports` independently: live authority validation, current
observations, locks and an external durable receipt store. Deserialization cannot
create those ports. The existing engine acquires the complete union, preflights all
effect groups, preserves original retry origins, and retains uncertain obligations.

## Process description

Canonical artifacts come exclusively from `workspace/render`. Its table-derived
`Bindings` remain separate from full `Executable`, `Argv`, `Environment` and `CWD`.
Boot reuses the existing pure provider launch conventions and registry posture
mapper. Legacy projection files, credential placeholders and implicit native
defaults never replace canonical artifacts. Required launch file locators must
name canonical planned files; bare skills feed the existing launch convention.

Claude print, streaming and PTY; Codex exec and app-server; and Antigravity print
have explicit process descriptions. Claude effort is serialized in canonical
native settings, Codex effort in its canonical config, and Antigravity effort in
the existing argv builder. These are resolved native inputs, not runtime acceptance
or model-support probes. OpenCode currently refuses an effort request because the
existing adapter has no evidenced effort binding; it does not silently drop it.

`Delivery` preserves prompt/system/resume inputs. Print argv follows the existing
prompt delimiter and resume placement. Streaming Claude describes a JSON user
message line. PTY text has no terminal-submit framing; RPC modes leave turn
submission to the caller. None is sent. Actual launch readiness and supported
model/effort behavior must be validated by the adopting host.
