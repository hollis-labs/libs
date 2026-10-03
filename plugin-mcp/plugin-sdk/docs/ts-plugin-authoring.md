# Author a bundled TypeScript plugin

Use the Folio `ts-plugin` preset to generate an independent author project with
schema sources, SDK harness tests, bundled server code and optional UI. The
preset requires a protocol-2 SDK release through its `sdk_spec` option. Until
that release exists, use an explicitly supplied local SDK tarball for development;
the default placeholder is intentionally not installable. No package is published
by the build or by Folio.

The Node-only `@hollis-labs/plugin-sdk/build` subpath supplies artifact collection,
SHA-256 inventory/tree digest, strict manifest-v2 author validation, canonical
JSON writing and bundle verification. It has zero runtime npm dependencies.
Import it from a build script, never from browser or worker code. Esbuild is an
author project's development dependency, separate from the SDK runtime.

```js
import { mkdtemp, mkdir } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { build } from 'esbuild';
import { writeManifest, verifyBundle } from '@hollis-labs/plugin-sdk/build';

const stage = await mkdtemp(join(tmpdir(), 'plugin-stage-'));
await mkdir(join(stage, 'bin'));
await build({
  entryPoints: ['src/server.ts'], outfile: join(stage, 'bin/server.js'),
  bundle: true, platform: 'node', format: 'esm', target: 'node22',
});
const declaration = {
  schema_version: 2, id: 'example.echo', name: 'Echo', version: '0.1.0',
  protocol: 2, runtime: 'subprocess',
  server: {
    runtime: 'node', entry: 'bin/server.js',
    engines: { node: { min: '22.0.0' } },
  },
  hosts: { example: { min: '0.1.0' } },
};
const manifest = await writeManifest(stage, declaration);
await verifyBundle(stage, manifest);
// Publish this private, verified snapshot using the host's reviewed install path.
// The caller owns cleanup of the scratch stage when it is no longer needed.
```

Build the worker to JavaScript before calculating hashes. Server entries live
under `bin/`; browser bundles and stylesheets live under `ui/`. No launcher,
external runtime command, raw permission flags or protocol-1 fallback is emitted.
The host resolves the runtime and computes permission flags before spawn from
approved grants and its own data/cache roots. Node is trusted-only with a Node 22
floor; untrusted JS requires host-approved Deno isolation. Bun has no tested
sandbox mapping. A browser `isolation` declaration is a preference:
`sandboxed-frame` is the host default, `main-origin` needs per-app approval.

## Shared artifact helper

- `collectFiles(directory)` inventories every regular payload file except root
  `plugin.yaml`, sorted by path; rejects symlinks, special files and privileged
  modes. It hashes complete bytes without line-ending normalization.
- `treeDigest(files)` computes the exact versioned binary stream described in
  [the manifest contract](manifest.md#artifact-digest-and-verification), binding
  path, raw SHA-256 and executable mode. It never mutates input order.
- `writeManifest(directory, declaration)` collects files, supplies `artifact`,
  validates, writes canonical JSON `plugin.yaml` exclusively (no overwrite), and
  verifies the staged tree. It requires a fresh stage with at least one payload.
- `verifyBundle(directory, reviewed?)` verifies the actual inventory/bytes/modes;
  optionally compares the canonical on-disk manifest with the reviewed value.
- `decodeManifest`, `validateManifest` and `encodeManifest` provide the shared
  author declaration checks. This is not a JSON Schema validator for tools or
  host extensions, nor an implementation of host trust or compatibility policy.

Known typed fields reject unknown keys, case aliases, explicit null and malformed
values. Decoding rejects duplicate JSON keys, excessive nesting, oversized input
and unsafe numeric values. Author objects must contain plain JSON values: no
undefined, cyclic references, BigInt, functions, nonfinite numbers or integers
outside JavaScript's safe range. Hosts that require larger opaque numeric values
must retain original JSON and use their own reviewed decoder rather than routing
it through the JavaScript author helper. The Go manifest package remains the
host-neutral source of this contract; the immutable `manifest/testdata/build-v1`
fixture proves byte/mode hashing and canonical output in both Go and Node.

Always stage privately and prevent concurrent mutation while verifying. The
helper is not an atomic filesystem lock or protection against a later path swap.
Publish and execute the same immutable verified snapshot. Review pins the
manifest separately from the payload tree digest, because `plugin.yaml` is
excluded to avoid a circular hash. A changed artifact or manifest needs the host's
normal review and compatibility checks, including local/dev plugins.

## One schema source

The preset's schema source defines MCP input objects and settings. Its generator
emits MCP `input_schema` objects, manifest-v2 `config.fields` and `config.secrets`,
and TypeScript author types. The settings shape uses the exact manifest names:
`boolean`, `integer`, `number`, `string` and `select`, with string defaults.
Secret declarations carry no value/default. There is no legacy ConfigFieldDef
DTO or runtime registration shortcut. Extend the generator's supported schema
subset explicitly when your plugin needs additional constructs; unsupported
constructs fail instead of silently producing different contracts.

Run the generated typecheck, harness tests, build and bundle verification after
changing schemas or worker code. Harness tests check author behavior; a stdio
smoke checks the built worker's actual transport. Protocol-2 worker smoke requires
an SDK containing the protocol-2 Serve/Init contract. A successful artifact build
alone is not protocol conformance.

## Development reload

The generated project builds and verifies a fresh stage before calling a
configured host-reload adapter (explicit command or URL). The adapter is an
author/developer action, not a plugin-granted host API. It must publish the stage
through the host's reviewed install path before requesting reload; never modify
an active reviewed bundle in place. A failed build/verify must not call reload.

Live protocol-2 reload remains held until the lifecycle controller and the host's
manifest/runtime adoption land. Existing Nanite reload/watch commands and its
reload HTTP endpoint are protocol-1-era evidence, not a compatible target for
these declarations. A stub host can verify build/adapter ordering and payload;
it does not certify a real host, sandbox or live reload. No host is migrated by
this preset. Unsupported reload must report unavailable, with no legacy fallback.
