/**
 * The loader's behaviour, including the defects it exists not to inherit.
 *
 * Tests named "regression:" pin something the reference implementation gets
 * wrong. They are the reason several of these behaviours are shaped the way
 * they are, so a later simplification that reintroduces the defect fails here
 * rather than in a host.
 *
 * Each of them was mutation-tested when written: the defect was reinstated in
 * `loader.ts`, the named test was confirmed to fail, and the mutation was
 * reverted. A regression test nobody has watched fail is decoration, and three
 * of these pin behaviour in states the happy path never reaches — which is
 * exactly where a test quietly stops testing anything. If you change what one
 * of them covers, break it on purpose first and check that it notices.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PROTOCOL, createPluginRegistry } from '../dist/index.js'

// --- helpers ---------------------------------------------------------------

/** A registry response, with sensible defaults for the parts a test ignores. */
function response({ plugins = {}, contributions = {}, protocol = PROTOCOL } = {}) {
  return { protocol, plugins, contributions }
}

/** An importer over a fixed url -> module map, recording every call. */
function importerOver(modules) {
  const calls = []
  const load = async (url) => {
    calls.push(url)
    const module = modules[url]
    if (!module) throw new Error(`no module at ${url}`)
    return typeof module === 'function' ? module() : module
  }
  load.calls = calls
  return load
}

/** A deferred promise, for driving out-of-order resolution deliberately. */
function deferred() {
  let resolve
  let reject
  const promise = new Promise((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const noStylesheets = { stylesheets: false }

// --- the model -------------------------------------------------------------

test('resolves a declared contribution from a plugin bundle', async () => {
  const Report = () => 'report'
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { ReportView: Report } }),
  })

  const result = await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { 'acme.report': { plugin_id: 'p', export: 'ReportView' } } },
    }),
  )

  assert.equal(result.accepted, true)
  assert.deepEqual(result.loaded, ['p'])
  assert.equal(result.declared, 1)
  assert.equal(result.resolved, 1)
  // The default adopt is the identity: a host that configures nothing gets
  // the raw export.
  assert.equal(registry.get('envelope', 'acme.report').value, Report)
  assert.equal(registry.get('envelope', 'acme.report').pluginId, 'p')
})

test('a contribution kind is an opaque string — the loader has no taxonomy', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: {
        'a-kind-nobody-has-heard-of': { k: { plugin_id: 'p', export: 'E' } },
      },
    }),
  )
  assert.ok(registry.get('a-kind-nobody-has-heard-of', 'k'))
})

test('host meta is carried verbatim and never interpreted', async () => {
  const meta = { slot: 'nav-rail', priority: 10, props: { deep: [1, null] } }
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { slot: { k: { plugin_id: 'p', export: 'E', meta } } },
    }),
  )
  assert.deepEqual(registry.get('slot', 'k').meta, meta)
})

test('adopt is where a host takes over execution', async () => {
  const seen = []
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
    adopt: (resolved) => {
      seen.push(resolved)
      return { wrapped: resolved.export }
    },
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  assert.equal(seen.length, 1)
  assert.equal(seen[0].exportName, 'E')
  assert.equal(registry.get('envelope', 'k').value.wrapped, seen[0].export)
})

// --- D1 --------------------------------------------------------------------

test('regression: a manifest-only change reaches the browser (D1)', async () => {
  // The reference implementation skips any plugin whose bundle URL is
  // unchanged and never re-reads its contributions, so a manifest edit that
  // does not rebuild the bundle is silently dropped. The whole point of a
  // host-manifest-authoritative model is that this cannot happen.
  const load = importerOver({ '/p.js?v=1': { A: () => 'a', B: () => 'b' } })
  const registry = createPluginRegistry({ ...noStylesheets, importModule: load })
  const plugins = { p: { bundle_url: '/p.js', bundle_version: '1' } }

  await registry.sync(
    response({ plugins, contributions: { envelope: { a: { plugin_id: 'p', export: 'A' } } } }),
  )
  assert.ok(registry.get('envelope', 'a'))
  assert.equal(registry.get('envelope', 'b'), undefined)

  // Same bundle, same version token — only the manifest changed.
  await registry.sync(
    response({
      plugins,
      contributions: {
        envelope: { a: { plugin_id: 'p', export: 'A' }, b: { plugin_id: 'p', export: 'B' } },
      },
    }),
  )

  assert.ok(registry.get('envelope', 'b'), 'a contribution added by the manifest never arrived')
  assert.deepEqual(load.calls, ['/p.js?v=1'], 'the unchanged bundle should not be re-imported')
})

test('regression: a contribution withdrawn by the manifest goes away (D1)', async () => {
  const load = importerOver({ '/p.js?v=1': { A: () => 'a', B: () => 'b' } })
  const registry = createPluginRegistry({ ...noStylesheets, importModule: load })
  const plugins = { p: { bundle_url: '/p.js', bundle_version: '1' } }

  await registry.sync(
    response({
      plugins,
      contributions: {
        envelope: { a: { plugin_id: 'p', export: 'A' }, b: { plugin_id: 'p', export: 'B' } },
      },
    }),
  )
  await registry.sync(
    response({ plugins, contributions: { envelope: { a: { plugin_id: 'p', export: 'A' } } } }),
  )

  assert.ok(registry.get('envelope', 'a'))
  assert.equal(registry.get('envelope', 'b'), undefined)
  assert.equal(registry.ownerOf('envelope', 'b'), undefined)
})

test('meta changes land while the adopted value keeps its identity', async () => {
  // Rebuilding the table on every sync must not remount a host's component:
  // the entry is fresh, the adopted value is memoized.
  const load = importerOver({ '/p.js?v=1': { E: () => 'x' } })
  let adoptions = 0
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: load,
    adopt: (resolved) => {
      adoptions += 1
      return { component: resolved.export }
    },
  })
  const plugins = { p: { bundle_url: '/p.js', bundle_version: '1' } }

  await registry.sync(
    response({
      plugins,
      contributions: { slot: { k: { plugin_id: 'p', export: 'E', meta: { priority: 1 } } } },
    }),
  )
  const first = registry.get('slot', 'k').value

  await registry.sync(
    response({
      plugins,
      contributions: { slot: { k: { plugin_id: 'p', export: 'E', meta: { priority: 99 } } } },
    }),
  )

  assert.equal(registry.get('slot', 'k').meta.priority, 99, 'meta must be the host latest')
  assert.equal(registry.get('slot', 'k').value, first, 'the adopted value must keep identity')
  assert.equal(adoptions, 1)
})

test('a re-imported bundle re-adopts, because the export is a new object', async () => {
  const first = { E: () => 'one' }
  const second = { E: () => 'two' }
  const load = importerOver({ '/p.js?v=1': first, '/p.js?v=2': second })
  const registry = createPluginRegistry({ ...noStylesheets, importModule: load })
  const contributions = { envelope: { k: { plugin_id: 'p', export: 'E' } } }

  await registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '1' } }, contributions }),
  )
  assert.equal(registry.get('envelope', 'k').value, first.E)

  await registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '2' } }, contributions }),
  )
  assert.equal(registry.get('envelope', 'k').value, second.E)
  assert.deepEqual(load.calls, ['/p.js?v=1', '/p.js?v=2'])
})

// --- D2 --------------------------------------------------------------------

test('regression: two plugins may export the same name (D2)', async () => {
  // The reference implementation keys resolved slot components by EXPORT NAME
  // in one global map, so two plugins that both export `Panel` overwrite each
  // other and the survivor is whichever loaded last.
  const onePanel = () => 'one'
  const twoPanel = () => 'two'
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/one.js': { Panel: onePanel }, '/two.js': { Panel: twoPanel } }),
  })

  await registry.sync(
    response({
      plugins: { one: { bundle_url: '/one.js' }, two: { bundle_url: '/two.js' } },
      contributions: {
        slot: {
          'one.panel': { plugin_id: 'one', export: 'Panel' },
          'two.panel': { plugin_id: 'two', export: 'Panel' },
        },
      },
    }),
  )

  assert.equal(registry.get('slot', 'one.panel').value, onePanel)
  assert.equal(registry.get('slot', 'two.panel').value, twoPanel)
})

// --- D3 --------------------------------------------------------------------

test('regression: an export is never inferred from an identifier (D3)', async () => {
  // The reference implementation infers a widget's export from its display
  // name and falls back to a PascalCase guess. A contribution with no export
  // is refused here rather than guessed at.
  const diagnostics = []
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { AcmeActivityWidget: () => 'w' } }),
    onDiagnostic: (event) => diagnostics.push(event),
  })

  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { widget: { 'acme-activity-widget': { plugin_id: 'p', export: '' } } },
    }),
  )

  assert.equal(registry.get('widget', 'acme-activity-widget'), undefined)
  assert.deepEqual(registry.refusals(), [
    { kind: 'widget', key: 'acme-activity-widget', pluginId: 'p', reason: 'invalid' },
  ])
  assert.ok(diagnostics.some((d) => d.type === 'contribution-refused'))
})

test('a missing named export skips that contribution and keeps its siblings', async () => {
  const diagnostics = []
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { Good: () => 'g' } }),
    onDiagnostic: (event) => diagnostics.push(event),
  })

  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: {
        envelope: {
          good: { plugin_id: 'p', export: 'Good' },
          bad: { plugin_id: 'p', export: 'Missing' },
        },
      },
    }),
  )

  assert.ok(registry.get('envelope', 'good'))
  assert.equal(registry.get('envelope', 'bad'), undefined)
  // A missing export is not a plugin-level failure: the plugin loaded.
  assert.deepEqual(registry.errors(), [])
  assert.ok(
    diagnostics.some((d) => d.type === 'export-missing' && d.exportName === 'Missing'),
    'a missing export must be reported, not silently dropped',
  )
  // Still attributable — the host can say whose contribution is absent.
  assert.equal(registry.ownerOf('envelope', 'bad'), 'p')
})

test('an object export is accepted — memo and forwardRef are not functions', async () => {
  const memoLike = { $$typeof: Symbol.for('react.memo') }
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: memoLike } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  assert.equal(registry.get('envelope', 'k').value, memoLike)
})

// --- D4 --------------------------------------------------------------------

test('regression: a plugin may never displace a reserved name, and the refusal is visible (D4)', async () => {
  // The reference enforces this structurally — its core registry is consulted
  // first — which is correct and completely silent. A host cannot tell that a
  // plugin tried.
  const diagnostics = []
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { Core: () => 'c', Mine: () => 'm' } }),
    reserved: (kind, key) => kind === 'envelope' && key === 'info-card',
    onDiagnostic: (event) => diagnostics.push(event),
  })

  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: {
        envelope: {
          'info-card': { plugin_id: 'p', export: 'Core' },
          'acme.thing': { plugin_id: 'p', export: 'Mine' },
        },
      },
    }),
  )

  assert.equal(registry.get('envelope', 'info-card'), undefined, 'a core name was displaced')
  assert.ok(registry.get('envelope', 'acme.thing'), 'an unclaimed name must still be claimable')
  assert.deepEqual(registry.refusals(), [
    { kind: 'envelope', key: 'info-card', pluginId: 'p', reason: 'reserved' },
  ])
  assert.ok(
    diagnostics.some((d) => d.type === 'contribution-refused' && d.reason === 'reserved'),
    'the refusal must be reported',
  )
  // Not attributed: a host must not credit a core name to the plugin that
  // tried to take it.
  assert.equal(registry.ownerOf('envelope', 'info-card'), undefined)
})

// --- failure isolation and attribution -------------------------------------

test('one bad bundle is one plugin absence, not a blank surface', async () => {
  const good = () => 'g'
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/good.js': { E: good } }), // '/bad.js' throws
  })

  const result = await registry.sync(
    response({
      plugins: { good: { bundle_url: '/good.js' }, bad: { bundle_url: '/bad.js' } },
      contributions: {
        envelope: {
          g: { plugin_id: 'good', export: 'E' },
          b: { plugin_id: 'bad', export: 'E' },
        },
      },
    }),
  )

  assert.equal(registry.get('envelope', 'g').value, good)
  assert.equal(registry.get('envelope', 'b'), undefined)
  assert.deepEqual(result.loaded, ['good'])
  assert.deepEqual(result.failed, ['bad'])
  assert.equal(registry.errors().length, 1)
  assert.equal(registry.errors()[0].pluginId, 'bad')
})

test('ownership survives a load failure, so the host can attribute the gap', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({}),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/missing.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  assert.equal(registry.get('envelope', 'k'), undefined)
  assert.equal(registry.ownerOf('envelope', 'k'), 'p')
})

test('a plugin with no bundle is declared, attributable and unresolved', async () => {
  const registry = createPluginRegistry({ ...noStylesheets, importModule: importerOver({}) })
  const result = await registry.sync(
    response({
      plugins: { p: {} },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  assert.equal(result.declared, 1)
  assert.equal(result.resolved, 0)
  assert.deepEqual(result.failed, [], 'a plugin with no browser half has not failed')
  assert.equal(registry.ownerOf('envelope', 'k'), 'p')
})

// --- concurrency -----------------------------------------------------------

test('concurrent syncs import a bundle once', async () => {
  const load = importerOver({ '/p.js': { E: () => 'x' } })
  const registry = createPluginRegistry({ ...noStylesheets, importModule: load })
  const doc = response({
    plugins: { p: { bundle_url: '/p.js' } },
    contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
  })

  await Promise.all([registry.sync(doc), registry.sync(doc)])
  assert.deepEqual(load.calls, ['/p.js'])
})

test('a superseded import does not commit when it finally resolves', async () => {
  const slow = deferred()
  const stale = () => 'stale'
  const fresh = () => 'fresh'
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: async (url) => {
      if (url === '/p.js?v=1') return slow.promise
      return { E: fresh }
    },
  })
  const contributions = { envelope: { k: { plugin_id: 'p', export: 'E' } } }

  const first = registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '1' } }, contributions }),
  )
  await registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '2' } }, contributions }),
  )
  slow.resolve({ E: stale })
  await first

  assert.equal(registry.get('envelope', 'k').value, fresh, 'an out-of-order import won')
})

test('a superseded import failure does not poison the newer bundle', async () => {
  const slow = deferred()
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: async (url) => {
      if (url === '/p.js?v=1') return slow.promise
      return { E: () => 'fresh' }
    },
  })
  const contributions = { envelope: { k: { plugin_id: 'p', export: 'E' } } }

  const first = registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '1' } }, contributions }),
  )
  await registry.sync(
    response({ plugins: { p: { bundle_url: '/p.js', bundle_version: '2' } }, contributions }),
  )
  slow.reject(new Error('stale bundle is gone'))
  await first

  assert.deepEqual(registry.errors(), [], 'a superseded failure was recorded against the live bundle')
  assert.ok(registry.get('envelope', 'k'))
})

// --- lifecycle -------------------------------------------------------------

test('a plugin dropped from the registry is unloaded', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  const result = await registry.sync(response())

  assert.equal(registry.get('envelope', 'k'), undefined)
  assert.equal(registry.ownerOf('envelope', 'k'), undefined)
  assert.deepEqual(result.loaded, [])
})

test('unload is a reload primitive — the next sync re-imports', async () => {
  const load = importerOver({ '/p.js': { E: () => 'x' } })
  const registry = createPluginRegistry({ ...noStylesheets, importModule: load })
  const doc = response({
    plugins: { p: { bundle_url: '/p.js' } },
    contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
  })

  await registry.sync(doc)
  registry.unload('p')
  assert.equal(registry.get('envelope', 'k'), undefined)

  await registry.sync(doc)
  assert.ok(registry.get('envelope', 'k'))
  assert.deepEqual(load.calls, ['/p.js', '/p.js'])
})

test('clear drops everything including the declared view', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  registry.clear()

  assert.equal(registry.get('envelope', 'k'), undefined)
  assert.equal(registry.ownerOf('envelope', 'k'), undefined)
  assert.deepEqual(registry.snapshot().plugins, [])
  assert.deepEqual(registry.snapshot().contributions, [])
})

// --- protocol --------------------------------------------------------------

test('an unknown protocol is refused whole and changes nothing', async () => {
  const diagnostics = []
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
    onDiagnostic: (event) => diagnostics.push(event),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )

  const result = await registry.sync(response({ protocol: PROTOCOL + 99 }))

  assert.equal(result.accepted, false)
  // A registry we cannot read is not evidence that the plugins went away.
  assert.ok(registry.get('envelope', 'k'), 'a refused response wiped live state')
  assert.ok(diagnostics.some((d) => d.type === 'protocol-refused'))
})

// --- subscription and ordering --------------------------------------------

test('subscribers are notified and version advances', async () => {
  let notifications = 0
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
  })
  const unsubscribe = registry.subscribe(() => {
    notifications += 1
  })
  const before = registry.version()

  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )

  assert.equal(notifications, 1)
  assert.ok(registry.version() > before)

  unsubscribe()
  await registry.sync(response())
  assert.equal(notifications, 1, 'unsubscribe did not take effect')
})

test('subscribe and version are stable references, as useSyncExternalStore needs', () => {
  const registry = createPluginRegistry(noStylesheets)
  assert.equal(registry.subscribe, registry.subscribe)
  assert.equal(registry.version, registry.version)
})

test('two registries do not share state', async () => {
  const load = importerOver({ '/p.js': { E: () => 'x' } })
  const a = createPluginRegistry({ ...noStylesheets, importModule: load })
  const b = createPluginRegistry({ ...noStylesheets, importModule: load })
  await a.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
    }),
  )
  assert.ok(a.get('envelope', 'k'))
  assert.equal(b.get('envelope', 'k'), undefined)
})

test('list returns a kind in the order the host declared it', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { A: () => 'a', B: () => 'b', C: () => 'c' } }),
  })
  await registry.sync(
    response({
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: {
        slot: {
          third: { plugin_id: 'p', export: 'C' },
          first: { plugin_id: 'p', export: 'A' },
          second: { plugin_id: 'p', export: 'B' },
        },
      },
    }),
  )
  assert.deepEqual(
    registry.list('slot').map((entry) => entry.key),
    ['third', 'first', 'second'],
  )
  assert.deepEqual(registry.list('nothing-of-this-kind'), [])
})

test('snapshot reports what is declared, what loaded and what was refused', async () => {
  const registry = createPluginRegistry({
    ...noStylesheets,
    importModule: importerOver({ '/p.js': { E: () => 'x' } }),
    reserved: (_kind, key) => key === 'core',
  })
  await registry.sync(
    response({
      plugins: {
        p: { bundle_url: '/p.js', runtime: { name: 'react', version: '^19.0.0' } },
        gone: { bundle_url: '/missing.js' },
      },
      contributions: {
        envelope: {
          k: { plugin_id: 'p', export: 'E' },
          core: { plugin_id: 'p', export: 'E' },
        },
      },
    }),
  )

  const snapshot = registry.snapshot()
  assert.equal(snapshot.protocol, PROTOCOL)
  assert.deepEqual(
    snapshot.plugins.map((plugin) => [plugin.id, plugin.loaded]),
    [
      ['p', true],
      ['gone', false],
    ],
  )
  // Carried and surfaced; nothing in this package enforces it.
  assert.deepEqual(snapshot.plugins[0].runtime, { name: 'react', version: '^19.0.0' })
  assert.deepEqual(
    snapshot.contributions.map((c) => [c.key, c.resolved]),
    [['k', true]],
  )
  assert.equal(snapshot.refusals.length, 1)
  assert.equal(snapshot.errors.length, 1)
})
