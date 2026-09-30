/**
 * The TypeScript half of the go/TS wire-contract check.
 *
 * `registry/testdata/contract/protocol-N/*.json` in the repo root is read here
 * AND by `registry/contract_test.go`. Each file is one raw wire response plus
 * what each view must make of it (`ts` is this half's expectation). Files are
 * FROZEN per released protocol: a later change that breaks a frozen file, even
 * one made to Go and TS together, must bump PROTOCOL — which is the host/loader
 * version-skew case a same-commit fixture cannot otherwise see. Only the
 * directory for the current PROTOCOL runs.
 *
 * Neither half counts fixtures; both fail when they find none.
 *
 * DELETION CONDITION (a bridge between two hand-authored views): delete when
 * either view is generated from the other, or both consume one schema.
 *
 * Known asymmetry, encoded in unknown-plugin.json and not fixed: Go's Validate
 * rejects a contribution naming an undescribed plugin; this loader declares it,
 * leaves it unresolved and does not refuse it.
 *
 * The path below reaches outside this package, so this test cannot run from the
 * published tarball (which ships no tests) or from a copy of the package alone.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { PROTOCOL, createPluginRegistry } from '../dist/index.js'

const dir = fileURLToPath(
  new URL(`../../../../registry/testdata/contract/protocol-${PROTOCOL}/`, import.meta.url),
)
const files = readdirSync(dir).filter((f) => f.endsWith('.json')).sort()

test('contract fixtures exist for the current protocol', () => {
  assert.notEqual(files.length, 0, `no contract fixtures in ${dir}: examined nothing is not a pass`)
})

for (const file of files) {
  const fixture = JSON.parse(readFileSync(join(dir, file), 'utf8'))

  test(`contract fixture ${file.replace(/\.json$/, '')}: ${fixture.description}`, async () => {
    const imports = []
    const registry = createPluginRegistry({
      stylesheets: false,
      // Record the exact URL the loader asks for; resolve to an empty module.
      importModule: async (url) => {
        imports.push(url)
        return {}
      },
    })

    const result = await registry.sync(fixture.response)

    assert.equal(result.accepted, fixture.ts.accepted, 'accepted')
    assert.equal(result.declared, fixture.ts.declared, 'declared')
    assert.deepEqual(
      registry.refusals().map(({ kind, key, reason }) => ({ kind, key, reason })),
      fixture.ts.refusals,
      'refusals',
    )
    if (fixture.ts.imports !== undefined) {
      assert.deepEqual([...imports].sort(), [...fixture.ts.imports].sort(), 'imported urls')
    }
  })
}
