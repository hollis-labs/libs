# Shared plugin declaration

Authors emit a declaration before a host starts the plugin. `manifest.Encode`
writes the canonical on-disk `plugin.yaml`: UTF-8 JSON, indented with two spaces,
with a trailing newline. Decode accepts equivalent JSON whitespace and ordering,
but broader YAML syntax is outside the contract. `go run ./examples/manifest`
demonstrates generation; a real build hashes its staged files first.

The schema version remains **2**, independently of the subprocess protocol.
This is a **clean break** within the new manifest-v2 workstream: `entrypoint`
is removed, `server` and `artifact` are required, and `protocol` must be **2**
(`manifest.RequiredProtocol`). Existing declarations with `entrypoint`, wire
protocol 1, no file inventory, or unordered/invalid bounds fail. No inference,
launcher bridge or fallback is provided. The Go `Entrypoint` type is removed.
The protocol-2 Init DTO and Go/TS Serve/corpus implementation belong to
CW-20261003-0172; this manifest change does not make the current protocol-1
Serve speak protocol 2. Do not use this manifest with that older Serve.

`manifest.Manifest` defines the common fields:

- Identity: `id`, `name`, `description`, `version`, `license`, `homepage`,
  `repository`. Versions are SemVer without a leading `v`; IDs may have dots.
- Execution: `runtime: subprocess`, `protocol: 2`, and
  `server: {runtime, engines, entry}`. See the pinned layout and runtime contract
  below. No command arguments or raw runtime permission flags are accepted.
- Optional `ui: {bundle, stylesheet, isolation}`: bundle is JavaScript under
  `ui/`, optional stylesheet is CSS under `ui/`; isolation is `sandboxed-frame` (host default) or
  `main-origin` (per-app override), a preference the host must explicitly accept or refuse.
- Required `artifact: {files, tree_sha256}`: exact regular-file inventory with
  SHA-256 and executable bits, and the deterministic digest defined below.
- Optional `hooks`: declarations with canonical dotted `name`, optional integer
  `priority` (omitted means 10; explicit zero survives), optional boolean `once`
  (omitted means false; explicit false survives), optional `view` token, `mode`, positive integer
  `timeout` in milliseconds, and `on_error: open|closed`. Modes are `sequential`,
  `parallel`, `bail`, `waterfall`, `async`, `after_commit`. Names are lowercase
  dotted segments with optional underscores, e.g. `context.pre_compact`; duplicate
  names are refused. A supplied view must be a nonblank token beginning with a
  letter or underscore, followed by letters, digits, underscores, dots or dashes.
  The host validates view availability and any required catalog view; the SDK
  never chooses a view. The host checks catalog kind/mode, name ownership, remote
  eligibility, schema, timeout cap and grants. SDK validation does not register
  callbacks, select a catalog or import plugin-hooks.
  Optional `schema_digest` is an opaque nonblank string token: omission is
  preserved, explicit `null` and blank strings are refused, and a supplied token
  is preserved exactly without trimming, case folding, parsing or an algorithm
  requirement. The host compares it byte-for-byte with the catalog's
  `schema_digest` at plan/compat; a mismatch fails planning. It is a compatibility
  assertion, not registration identity: identity remains the owner tuple plus
  registration name (the manifest hook name). The SDK never interprets or
  compares the digest. Absence makes no schema-digest assertion.
- `capabilities`: existing `subprocess.CapabilityRequest` values. Names and
  metadata belong to the host; declaration grants nothing.
- `config.fields`: named ordinary settings with type, label, description,
  required flag, string default, optional environment name and select options.
  Settings use strings because `InitParams.Config` carries strings.
- `config.secrets`: named secret declarations with label, description, required
  flag and optional environment name. Values and defaults are forbidden.
  A key cannot be both a field and a secret. The host resolves secret values.
- `tools`: name, description, an inline JSON Schema with `type: object`, and
  a required `effect` in the host's vocabulary. The SDK checks structure only;
  it does not define effects, infer them, or grant execution authority. Optional
  `annotations` carries MCP hints checked for consistency as described below.
- `hosts`: a map of host names to inclusive `min`/`max` SemVer bounds. At least
  one bound is required for each host. Hosts interpret and enforce their own
  public contract versions through `CheckCompatibility`, with explicit
  prerelease policy. An absent actual version or unknown required name fails.
- `cerberus`, `tangent`, `nanite`: optional opaque JSON objects with host-owned
  registrations. Each extension requires a corresponding `hosts` entry. The
  host must decode and validate its own block before applying registrations.
  `DecodeExtension` supplies the same strict decoding checks for a host-owned
  struct; the host validates that struct's meaning separately.

Archives, signatures and catalog tiers remain distribution concerns. The
manifest inventory verifies unpacked payload bytes; it is not a signature or
an archive checksum.

## Layout and runtime contract

A bundle contains `plugin.yaml`, `bin/` server files, optional `ui/` browser
files, and any inventoried supporting files. An entry under `dist/` is refused.
All inventory paths are relative, slash-separated ASCII paths with letters,
digits, dots, underscores and dashes. Paths must already be canonical: no `.`
or `..` components, duplicate separators, absolute/drive/UNC paths, backslashes,
spaces, control characters or shell expressions. Duplicate paths (case folded)
and file/directory collisions are refused. Symlinks and special files have no
representation in the inventory and are rejected by `VerifyBundle`.

`server.runtime` is `node`, `deno`, `bun` or `binary`. For JS runtimes, `entry`
is a precompiled `.js`, `.mjs` or `.cjs` file under `bin/`, not a shebang launcher,
TypeScript source or a runtime command to find through PATH. Binary entry is a
native executable under `bin/`. Hosts resolve their runtime themselves and
launch it directly against the verified entry. Structural validation cannot
prove file contents are JavaScript/native code; host resolution must refuse
launcher scripts masquerading as binary entries.

`engines` is a map with **exactly one key matching the selected runtime**:

```json
{"runtime":"node","engines":{"node":{"min":"22.0.0","max":"26.99.99"}},"entry":"bin/server.js"}
```

Each value uses the same normalized inclusive `min`/`max` structure as `hosts`.
At least one side is required; a missing side is unbounded. Values are strict
SemVer 2.0.0 without `v`; `min > max` fails. Numeric core/prerelease identifiers
use semantic ordering without integer overflow; build metadata is ignored.
No npm range strings (`^`, `~`, wildcard, disjunction) or alternate engine
names are accepted. Independently of declared bounds, actual Node versions
must be at least `22.0.0`; even a max-only Node range cannot bypass that floor.
For `binary`, the `binary` key names a versioned native-runner contract supplied
by the host, not the plugin version, Go compiler or OS version. Hosts without
that explicit contract must refuse an unresolved binary engine requirement.

`Manifest.CheckCompatibility(Compatibility{Hosts, Engines, AllowPrerelease})`
checks every declared host/engine against host-resolved actual versions, before
spawn. `Hosts` supplies public host contract versions, not app binary versions.
The host obtains runtime versions without running plugin code. Missing,
malformed and out-of-range values fail. Prereleases require an explicit host
opt-in in addition to satisfying the bounds. 0.x and local/dev plugins use the
same checks; there is no unsafe/dev bypass. Hosts may apply stricter policy.

The owner-approved trust policy requires Deno for untrusted JS plugins; Node
is trusted-only and requires Node >=22. The schema does not assert trust or
make an isolation guarantee. Bun is accepted structurally but **untested**:
there is no Bun spike or approved sandbox mapping; a host must choose whether
its policy supports it. Hosts compute Node/Deno permissions from approved grants
and host-chosen data/cache roots **before spawn**. The manifest carries no raw
runtime flags, ambient credentials or claim of permission.

## Artifact digest and verification

`artifact.files` is a nonempty list of `{path, sha256, executable?}`. `sha256`
is exactly 64 lowercase hex characters, SHA-256 over the complete file bytes.
Omitted `executable` means false; true means at least one execute permission
bit is set. Binary server entries require true; JS server and declared UI files
require false. All declared entry/UI/stylesheet paths must appear in the list.
Every regular payload file must be listed, including imports and assets.

`artifact.tree_sha256` is lowercase SHA-256 of this exact byte stream:

1. ASCII domain `plugin-sdk-artifact-v1` followed by one NUL byte.
2. Sort file records by ascending ASCII path bytes (case sensitive).
3. For each record append a **4-byte unsigned big-endian** path byte length,
   the path bytes, the **32 raw SHA-256 bytes** decoded from `sha256`, then one
   byte for executable (`0x00` false or `0x01` true).

No delimiters, record count, JSON, hex text hashes, timestamps, directory entries
or other permission bits enter this stream. File list order does not affect
it. Empty directories carry no digest material. `TreeDigest` computes this
algorithm without mutating the input; `Artifact.Validate` checks the advertised
digest against the inventory.

`plugin.yaml` is excluded from the file list and tree digest to avoid a circular
hash. **Review must separately pin the manifest** (its exact bytes or canonical
`Encode` bytes) together with the artifact digest, identity/version, grants and
runtime/isolation choice. Tree digest alone is not a manifest identity.
`Manifest.VerifyBundle(dir)` also decodes the on-disk manifest and compares its
canonical encoding with the reviewed receiver, so a different declaration beside
the same payload is rejected. It rejects missing/extra files, links (including
directory links), special files, setuid/setgid, wrong executable bits and changed
SHA-256 bytes. It does not install, fetch or execute anything.

The caller must supply a **private immutable staged tree**, prevent mutation
during verification and launch the same verified snapshot. Verification is not
an atomic filesystem lock or protection against a later path swap. Host review,
immutable publication and execution-boundary revalidation remain host duties.


## Tool annotations

`annotations` is an optional object using the MCP field names: optional string
`title` and optional booleans `readOnlyHint`, `destructiveHint`, `idempotentHint`
and `openWorldHint`. Projection of this object to MCP uses the same keys and
values. Explicit `false` survives encoding; omitted fields acquire no defaults.
Omitting the object leaves existing manifest behavior and output unchanged.
This change does not implement host registration. Hints never authorize
execution or lower policy: `effect` stays authoritative.

The only cross-field rule is that `readOnlyHint: true` cannot accompany
`destructiveHint: true`. The SDK refuses that conflict with the tool name and
hints in the error. All other combinations are accepted, including idempotent
read or destructive tools. Effects remain an open host vocabulary and are not
compared with hints. An empty annotation object is valid.

For example, a repeatable write can declare:

```json
{"name":"notes_save","description":"Save notes","input_schema":{"type":"object"},"effect":"write","annotations":{"title":"Save notes","readOnlyHint":false,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}}
```

### Compatibility with older strict hosts

The historical annotations addition accepted previous execution envelopes.
This task replaces those envelopes as described above. Within the new envelope,
a manifest that **uses** `annotations` requires the SDK release carrying this field. Older strict hosts
refuse it by name with `manifest: unknown field "annotations"`; they do not
silently ignore it. This is not forward compatible with those hosts. No release
number is assigned here, and the historical addition changed neither schema version 2 nor subprocess
protocol 1. The new execution contract independently requires protocol 2. The rollout order is: SDK release carrying annotations, then the Nanite SDK
bump, then updated nanite-plugins manifests. Phase A therefore requires SDK,
host and plugin updates; it is not host-only. Each host adopts on its own
schedule, with no lockstep release.

Compatibility fixtures live in `manifest/testdata/annotations/`: manifests with
and without hints, plus `older-strict-host-error.txt`, the frozen diagnostic
observed from the pre-change decoder. That diagnostic is documentation of
version skew, not a test that builds or simulates old SDK code.

### Historical tool definitions

`manifest/testdata/annotations/nanite-pre-cutover.json` freezes the exact
`context_pin`, `context_unpin` and `reminder_set` names, descriptions, input
schemas and annotations. Source: Nanite
`2de304e3d1cebe8d875f7806c03ec0eae8f6b8fe`, the commit built into the
pre-cutover binary with SHA256 prefix `ef20f9be` (a binary hash, not a Git
revision). Definitions come from `internal/selftools/self_tools.go`; the four
boolean hints come from `internal/mcpserver/annotations.go` and are applied
unchanged by `buildTool` in `internal/mcpserver/server.go`. The source has no
title for these tools. Zero-value false hints are preserved explicitly.

Tests preserve the historical fixture files, adapt only their old execution
envelope to the new required server/artifact/protocol shape, and validate the
unchanged tool definitions through a manifest roundtrip, preserving the MCP annotation keys and values. Historical MCP
definitions have no `effect` field; the test supplies an opaque host-defined
effect rather than inferring historical metadata. No Nanite checkout or binary
is required to run the test.

## Validation and enforcement

`Validate` rejects malformed common declarations without reading files or
starting the plugin. `Decode` also refuses unknown fields (including alternate
case spellings), duplicate keys at every level, trailing input, excessive
nesting and declarations exceeding `MaxBytes`. Host extensions and tool schemas
remain raw JSON, preserving their values rather than decoding numbers to floats.

Known typed fields reject explicit null; omit optional fields instead. Raw
host extension/schema JSON remains opaque and may contain null values.

A structurally valid manifest is still a claim by executable code. The host
must check its own extension and tool schemas, resolve and confine the entrypoint
on disk (including symlinks), verify bundle digests at load, limit the launch
environment, resolve only declared secrets, obtain install confirmation and
apply its own acknowledgment rules. None of those boundaries is established
by calling SDK validation. Data and cache directories are supplied through
`InitParams`, outside the bundle; upgrading executable code must preserve data.
