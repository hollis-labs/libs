# Shared plugin declaration

A plugin binary emits its declaration before a host starts it, conventionally
through `--manifest` or a `write-dist` command. Use `manifest.Encode` to write
`plugin.yaml`; `go run ./examples/manifest` demonstrates generation. Output is
JSON, which is valid YAML, so the SDK needs no YAML dependency. `manifest.Decode`
accepts the generated JSON format. A host accepting broader YAML syntax must
reject duplicate YAML keys, convert it to JSON, and use the same decoder.

The manifest schema version is **2**. It is separate from the subprocess wire
protocol version, which is `subprocess.ProtocolVersion`. Unknown schema versions
and legacy host dialects are errors; there is no inference or fallback.

`manifest.Manifest` defines the common fields:

- Identity: `id`, `name`, `description`, `version`, `license`, `homepage`,
  `repository`. Versions are SemVer without a leading `v`; IDs may have dots.
- Execution: `runtime: subprocess`, integer `protocol`, and
  `entrypoint: {command, args}`. Command is a relative executable path inside
  the bundle, never a shell string or a program found through `PATH`.
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
  compatibility ranges, bound ordering and prerelease policy.
- `cerberus`, `tangent`, `nanite`: optional opaque JSON objects with host-owned
  registrations. Each extension requires a corresponding `hosts` entry. The
  host must decode and validate its own block before applying registrations.
  `DecodeExtension` supplies the same strict decoding checks for a host-owned
  struct; the host validates that struct's meaning separately.

No archive, signature, catalog tier or compiled-in runtime belongs to this
contract. Archives and checksums belong to distribution catalogs.

## Tool annotations

`annotations` is an optional object with optional boolean fields `readOnly`,
`destructive`, `idempotent` and `openWorld`. Explicit `false` survives encoding;
omitted fields acquire no defaults. Omitting the object leaves existing manifest
behavior and output unchanged. The SDK carries hints; hosts map them to MCP
annotations when registering tools. This change does not implement host mapping.
Hints never authorize execution or lower policy: `effect` stays authoritative.

For the conventional effects, any supplied hint must follow this matrix:

| Effect | readOnly | destructive | idempotent | openWorld |
|---|---|---|---|---|
| read | true | false | false | either |
| write | false | false | either | either |
| destructive | false | true | false | either |

Every field may be omitted. `idempotent: true` is meaningful only for `write`;
false is neutral. `destructive: true` cannot accompany `readOnly: true`.
The SDK refuses inconsistencies with the tool name and offending hint in the
error. An empty annotation object is valid. Effects remain an open host
vocabulary: custom effects are accepted without semantic hints or with only
`openWorld`, but supplied `readOnly`, `destructive` or `idempotent` hints are
refused because the SDK cannot establish consistency for a custom effect.
MCP `title` is outside this four-field object.

For example, a repeatable write can declare:

```json
{"name":"notes_save","description":"Save notes","input_schema":{"type":"object"},"effect":"write","annotations":{"readOnly":false,"destructive":false,"idempotent":true,"openWorld":false}}
```

### Compatibility with older strict hosts

The new decoder reads existing manifests unchanged. A manifest that **uses**
`annotations` requires the SDK release carrying this field. Older strict hosts
refuse it by name with `manifest: unknown field "annotations"`; they do not
silently ignore it. This is not forward compatible with those hosts. No release
number is assigned here, and neither schema version 2 nor subprocess protocol 1
changes. Host releases must disclose support before plugin authors emit hints.

Compatibility fixtures live in `manifest/testdata/annotations/`: manifests with
and without hints, plus `older-strict-host-error.txt`, the frozen diagnostic
observed from the pre-change decoder. That diagnostic is documentation of
version skew, not a test that builds or simulates old SDK code.

## Validation and enforcement

`Validate` rejects malformed common declarations without reading files or
starting the plugin. `Decode` also refuses unknown fields (including alternate
case spellings), duplicate keys at every level, trailing input, excessive
nesting and declarations exceeding `MaxBytes`. Host extensions and tool schemas
remain raw JSON, preserving their values rather than decoding numbers to floats.

A structurally valid manifest is still a claim by executable code. The host
must check its own extension and tool schemas, resolve and confine the entrypoint
on disk (including symlinks), verify bundle digests at load, limit the launch
environment, resolve only declared secrets, obtain install confirmation and
apply its own acknowledgment rules. None of those boundaries is established
by calling SDK validation. Data and cache directories are supplied through
`InitParams`, outside the bundle; upgrading executable code must preserve data.
