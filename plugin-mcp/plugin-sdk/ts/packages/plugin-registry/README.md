# @hollis-labs/plugin-registry

The browser registry, loader and optional React adapter for
`github.com/hollis-labs/plugin-sdk`. The Go and TypeScript views share the
[registry contract](../../../docs/protocol/registry-v2.md) and conformance fixtures.
The core has no runtime dependencies. React `^19` is an optional peer behind
`./react`. This is a breaking replacement for the registry shape in 0.1.0;
there is no compatibility fallback. Documents require `registry_version: 2`;
legacy `protocol` keys, dual-key documents and unsupported versions fail with
`ErrRegistryVersion`.

```sh
npm i @hollis-labs/plugin-registry
```

## Host admission

The registry describes contributions by owner, generation and local key.
The manifest is authoritative: importing a bundle never discovers additional
contributions. Hosts explicitly opt into kind and region descriptors and
supply their own metadata validator for nonempty schemas. Unsupported optional
entries produce named refusals; a required refusal stops preflight and keeps
the serving registry intact. Capability declarations carry information; hosts
remain responsible for authorization and isolation.

```ts
import { createPluginRegistry, type KindDescriptor, type RegionDescriptor } from '@hollis-labs/plugin-registry'

const panel: KindDescriptor = {
  schema_version: 1,
  metadata_schema: {},
  representations: ['component'],
  regions: ['rail'],
  required_capabilities: [],
}
const rail: RegionDescriptor = {
  kinds: ['panel'],
  representations: ['component'],
  context_schema: {},
  ordering: 'manifest' as const,
}
const registry = createPluginRegistry({
  kinds: { panel },
  regions: { rail },
  runtimes: { react: '19.1.0' },
  stylesheets: false,
  onDiagnostic: (event) => report(event),
})

// Pass original JSON text so duplicate keys can be detected before JSON.parse.
const raw = await fetch('/api/plugins/registry').then((r) => r.text())
await registry.sync(raw)
const contribution = registry.get('panel', 'acme/main')
```

`sync` also accepts a validated object, but an object cannot retain duplicate
keys discarded by a prior JSON parser. Original text is required at the wire
boundary. Validation rejects case-insensitive duplicate keys at every nesting
level, mis-cased known fields, unsafe revisions, unknown owners and malformed
representations. Runtime bounds use normalized semantic versions, inclusive
endpoints and explicit prerelease opt-in.

## Verified bundles and lifecycle

Component bundles require an exact `sha256:` digest. The loader fetches bytes
once, checks that digest, and imports the same bytes. Its default importer uses
an immutable data URL, so bundles must be self-contained and the host CSP must
permit that URL. Relative module imports and automatic runtime sharing are not
provided. A custom `importModule` receives a `VerifiedBundle`; it must execute
`bundle.bytes`, never refetch `bundle.sourceUrl`. Hosts can override
`fetchBundle` to control transport and credentials.

Preflight checks admission, runtime compatibility and integrity before retiring
an active owner. Replacement then fences captured entries and disposes resources
in reverse acquisition order before importing or adopting the replacement.
Import or adoption failure after revocation leaves the old generation revoked.
Cleanup failures quarantine the owner, and disposal continues for other
resources. Unchanged contributions retain their values and disposer accounting.
The host supplies `dispose` for resources its `adopt` callback creates.

`unload(owner, generation?)` fences immediately and aborts cooperative pending
work before awaiting disposal. Late completions cannot reactivate tombstoned
generations. `clear()` retires the host epoch; reconnect with a fresh epoch.
Cancellation is cooperative: a custom importer or disposer that never settles
can hold subsequent activation work. Tombstones and cleanup quarantines live
for the lifetime of a registry instance.

## React

```ts
import {
  createReactPluginRegistry,
  usePluginContribution,
} from '@hollis-labs/plugin-registry/react'

const registry = createReactPluginRegistry({
  kinds: { panel }, regions: { rail }, runtimes: { react: '19.1.0' },
})

function Slot() {
  const contribution = usePluginContribution(registry, 'panel', 'acme/main')
  if (!contribution) return null
  const Component = contribution.value
  return <Suspense fallback={null}><Component /></Suspense>
}
```

The adapter wraps component exports in `React.lazy` and checks the captured
entry's active generation on render. An old captured component renders nothing
after revocation. Declarative data and browser-safe handler bindings remain
data; the SDK does not execute handlers or select a winner for a host surface.

## Inspecting the registry

`get` and `list` return active adopted contributions. `ownerOf` attributes
listed declarations, including unresolved exports. `snapshot` exposes declared
entries, resolution state and owner generations; `refusals` and `errors` expose
named admission and lifecycle failures. Entries carry host-declared `status` and
optional `status_reason`. Only `accepted` entries resolve. `declared_not_selected`,
`unavailable`, and unknown statuses remain listed and inactive; unknown values
emit `status-diagnostic` with reason `unknown-status`. A retained `refused` entry
must match the authoritative top-level refusal. Status never bypasses admission
checks or adds SDK selection logic. `subscribe` and `version` support
external-store integrations. Hosts define the meaning of kinds, ordering,
selection, schema compilation, trust, CSP and shared runtimes.

Release history is in [CHANGELOG.md](./CHANGELOG.md). MIT — see
[LICENSE](./LICENSE).
