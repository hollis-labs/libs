/**
 * The wire contract's own invariants.
 *
 * The Go view of this contract lives in `libs/plugin-sdk/registry/` and pins
 * the same protocol number in `TestProtocolLockedAt1`. Neither side generates
 * the other and there is no shared fixture: each side pins the version and
 * round-trips its own types, so a host and a loader built at different
 * versions disagree about the protocol number — which a loader reports at
 * runtime — rather than about a field, which nothing would notice.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PROTOCOL } from '../dist/index.js'

test('protocol is locked at 1', () => {
  assert.equal(
    PROTOCOL,
    1,
    'bump registry.Protocol in the Go half in the same change, or a host and a loader stop agreeing',
  )
})

test('a full response survives a JSON round trip unchanged', () => {
  const response = {
    protocol: PROTOCOL,
    plugins: {
      'acme.widgets': {
        bundle_url: '/api/plugins/acme.widgets/ui/index.js',
        stylesheet_url: '/api/plugins/acme.widgets/ui/index.css',
        bundle_version: '1757600000000',
        runtime: { name: 'react', version: '^19.0.0' },
      },
    },
    contributions: {
      envelope: {
        'acme.report': { plugin_id: 'acme.widgets', export: 'ReportView', meta: { version: 2 } },
      },
      slot: {
        'acme.nav': {
          plugin_id: 'acme.widgets',
          export: 'AcmeNavPage',
          meta: { slot: 'nav-rail', priority: 10, label: 'Acme', props: {} },
        },
      },
    },
  }

  assert.deepEqual(JSON.parse(JSON.stringify(response)), response)
})

test('contribution meta is opaque — arbitrary host shapes survive', () => {
  const meta = { nested: { deep: [1, 2, { x: null }] }, unicode: 'café' }
  const round = JSON.parse(
    JSON.stringify({
      protocol: PROTOCOL,
      plugins: { p: { bundle_url: '/p.js' } },
      contributions: { whatever: { k: { plugin_id: 'p', export: 'E', meta } } },
    }),
  )
  assert.deepEqual(round.contributions.whatever.k.meta, meta)
})
