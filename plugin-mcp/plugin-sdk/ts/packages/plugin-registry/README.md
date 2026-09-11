# @hollis-labs/plugin-registry

The browser half of the plugin contract. The Go half is
`github.com/hollis-labs/plugin-sdk` in the same repository, and
`registry/registry.go` there is the other view of the wire contract
`src/types.ts` declares here.

A host publishes a registry describing what its plugins registered. This
package fetches nothing and decides nothing — you hand it that registry and it
dynamic-imports each plugin's ES module, pulls the named exports the registry
names, and keeps a table you can look contributions up in. One bad bundle is
one plugin's absence, not a blank surface.

## The model

**The host manifest is authoritative.** A plugin does not declare at runtime
what it registers; the host does, and this loader never learns a registration
from a bundle it imported.

**A plugin may claim an unclaimed name and may never displace a core one.**
Supply `reserved` and a contribution claiming one of your core names is
refused, recorded and reported rather than silently shadowed.

**Contributions are flat.** One contribution per `(kind, key)`. A kind is an
open string you choose; its `meta` is opaque JSON this package never reads.
Where you group or order contributions — an ordered rail, a priority-sorted
toolbar — the group and the order live in `meta`, because they are your
taxonomy and no loader needs them.

## Use

```ts
import { createPluginRegistry } from '@hollis-labs/plugin-registry'

const registry = createPluginRegistry()

const response = await fetch('/api/plugins/registry').then((r) => r.json())
await registry.sync(response)

registry.get('envelope', 'acme.report')?.value
```

It works with no configuration: it imports modules, injects stylesheets,
resolves exports and notifies subscribers. Every option is an override.

```ts
const registry = createPluginRegistry({
  // Turn a resolved export into what you render. Default: the raw export.
  // This is where a host that must contain plugin code puts that containment.
  adopt: (resolved) => wrap(resolved.export),

  // Names your core owns.
  reserved: (kind, key) => kind === 'envelope' && CORE_KINDS.has(key),

  // Injectable so tests need no network.
  importModule: (url) => import(url),

  // A `<link>` in document.head by default. `false` touches no DOM at all.
  stylesheets: false,

  // Structured, because a library that writes to the console is one you
  // cannot quiet.
  onDiagnostic: (event) => log(event),
})
```

### React

```ts
import {
  createReactPluginRegistry,
  usePluginContribution,
} from '@hollis-labs/plugin-registry/react'

const registry = createReactPluginRegistry() // adopt = React.lazy wrapping

function Slot({ kind, id }) {
  const contribution = usePluginContribution(registry, kind, id)
  if (!contribution) return <Missing owner={registry.ownerOf(kind, id)} />
  const Component = contribution.value
  return (
    <Suspense fallback={null}>
      <Component />
    </Suspense>
  )
}
```

React is an optional peer behind the `./react` subpath; the core entry point
has no dependencies.

## Attributing what is missing

`ownerOf(kind, key)` answers for any contribution the host declared, whether
or not its bundle loaded — so a surface that is empty because a plugin failed
can say whose fault it is. `errors()` names the plugins whose bundle did not
import, `refusals()` the contributions that were refused, and `snapshot()`
gives you everything at once for devtools.

## What this package does not know

What a contribution *kind* means; your trust, capability or isolation model;
where bundles are served from and under what CSP; and how a bundle obtains
shared runtime dependencies such as a React copy. Each of those has a
different right answer in every host, and a shared package that learned one
host's answer would impose it on the rest.
