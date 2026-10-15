import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { validateJSON, decodeJSONObject } from '../dist/strict-json.js';

const fixtures = JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/strict-json.json', import.meta.url), 'utf8'));
for (const fixture of fixtures) {
  test(`strict JSON: ${fixture.name}`, () => {
    const run = () => fixture.fields ? decodeJSONObject(fixture.input, fixture.fields) : validateJSON(fixture.input);
    if (fixture.valid) assert.doesNotThrow(run);
    else assert.throws(run, SyntaxError);
  });
}
test('raw numeric tokens survive without floating point conversion', () => {
  const fields = decodeJSONObject('{"generation":9007199254740991.4}', ['generation']);
  assert.equal(fields.get('generation'), '9007199254740991.4');
});
