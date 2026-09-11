/**
 * The React adapter.
 *
 * These cover the adapter's wiring — that `adopt` is pre-supplied, that it
 * produces a lazy element type, and that a host's own `adopt` still wins.
 * The hooks are thin `useSyncExternalStore` wrappers and are NOT exercised
 * here: this workspace has no renderer and no DOM, and adding one to assert
 * that `useSyncExternalStore` re-renders would be testing React. What the
 * hooks depend on — that `subscribe` and `version` are stable references and
 * that the registry notifies — is covered in loader.test.js.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PROTOCOL } from '../dist/index.js'
import { createReactPluginRegistry, reactAdopt } from '../dist/react.js'

const LAZY = Symbol.for('react.lazy')

test('reactAdopt produces a lazy element type', () => {
  const Component = () => null
  const adopted = reactAdopt({
    kind: 'envelope',
    key: 'k',
    pluginId: 'p',
    exportName: 'E',
    export: Component,
  })
  assert.equal(adopted.$$typeof, LAZY)
})

test('createReactPluginRegistry adopts into React.lazy by default', async () => {
  const registry = createReactPluginRegistry({
    stylesheets: false,
    importModule: async () => ({ E: () => null }),
  })

  await registry.sync({
    protocol: PROTOCOL,
    plugins: { p: { bundle_url: '/p.js' } },
    contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
  })

  assert.equal(registry.get('envelope', 'k').value.$$typeof, LAZY)
})

test("a host's own adopt still wins", async () => {
  const registry = createReactPluginRegistry({
    stylesheets: false,
    importModule: async () => ({ E: () => null }),
    adopt: (resolved) => ({ contained: resolved.exportName }),
  })

  await registry.sync({
    protocol: PROTOCOL,
    plugins: { p: { bundle_url: '/p.js' } },
    contributions: { envelope: { k: { plugin_id: 'p', export: 'E' } } },
  })

  assert.deepEqual(registry.get('envelope', 'k').value, { contained: 'E' })
})

test('the React adapter keeps the core defaults it did not override', async () => {
  const registry = createReactPluginRegistry({
    stylesheets: false,
    importModule: async () => ({ Good: () => null }),
    reserved: (_kind, key) => key === 'core',
  })

  await registry.sync({
    protocol: PROTOCOL,
    plugins: { p: { bundle_url: '/p.js' } },
    contributions: {
      envelope: {
        core: { plugin_id: 'p', export: 'Good' },
        mine: { plugin_id: 'p', export: 'Good' },
      },
    },
  })

  assert.equal(registry.get('envelope', 'core'), undefined)
  assert.ok(registry.get('envelope', 'mine'))
})
