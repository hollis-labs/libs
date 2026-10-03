import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdir, readFile } from 'node:fs/promises';
import { PassThrough, Writable } from 'node:stream';
import { createInterface } from 'node:readline';
import { serve, FrameTooLargeError } from '../dist/index.js';
import { fixturePlugin } from './fixtures.js';

const directory = new URL('../../../../docs/protocol/v2/transcripts/', import.meta.url);
const names = (await readdir(directory)).filter(name => name.endsWith('.json')).sort();
assert.ok(names.length, 'missing shared protocol corpus');
for (const name of names) {
  const fixture = JSON.parse(await readFile(new URL(name, directory), 'utf8'));
  // Status gates availability; level gates obligation. This v1 regression
  // runner still asserts quirks, explicitly recording why it passes them.
  assert.ok(['observed', 'proposed'].includes(fixture.status ?? 'observed'));
  assert.ok(['normative', 'observed-quirk'].includes(fixture.level), `${name}: missing/invalid level`);
  const levels = fixture.steps.map(step => step.level ?? fixture.level);
  for (const [index, level] of levels.entries()) {
    assert.ok(['normative', 'observed-quirk'].includes(level), `${name} step ${index + 1}: invalid level`);
    if (level === 'observed-quirk') assert.ok((fixture.steps[index].preferred ?? fixture.preferred)?.trim(), `${name} step ${index + 1}: missing preferred note`);
  }
  const quirks = levels.flatMap((level, index) => level === 'observed-quirk' ? [index + 1] : []);
  test(`shared protocol: ${name}${quirks.length ? ` [COPIED GO V1 QUIRKS: steps ${quirks.join(',')}]` : ' [NORMATIVE]'}`, { skip: fixture.status === 'proposed' ? fixture.finding : false, timeout: 10000 }, async t => {
    const input = new PassThrough();
    const output = new PassThrough();
    const lines = createInterface({ input: output, crlfDelay: Infinity });
    const replies = lines[Symbol.asyncIterator]();
    t.after(() => { input.destroy(); output.destroy(); lines.close(); });
    const plugin = fixturePlugin(fixture.profile);
    const done = serve(plugin, { input, output, stderr: new Writable({ write(_b, _e, cb) { cb(); } }) }).then(
      () => { output.end(); return undefined; },
      error => { output.end(); return error; },
    );
    for (const step of fixture.steps) {
      let frame = step.raw ?? JSON.stringify(step.send);
      if (step.pad_bytes) frame += ' '.repeat(step.pad_bytes - Buffer.byteLength(frame));
      if (step.repeat) frame = frame.repeat(step.repeat);
      // The reader may terminate while accepting an oversized frame.
      input.write(frame + '\n');
      if (step.expect === undefined) continue;
      const reply = await replies.next();
      assert.equal(reply.done, false, 'stdout closed before expected reply');
      const actual = JSON.parse(reply.value);
      if (step.message_prefix) {
        assert.ok(actual.error.message.startsWith(step.message_prefix), actual.error.message);
        delete actual.error.message;
      }
      assert.deepEqual(actual, step.expect);
    }
    input.end();
    for await (const extra of { [Symbol.asyncIterator]: () => replies }) assert.fail(`unexpected reply: ${extra}`);
    const error = await done;
    if (fixture.termination === 'frame-too-large') assert.ok(error instanceof FrameTooLargeError);
    else assert.equal(error, undefined);
    if(fixture.effects) assert.deepEqual(plugin.effects(),fixture.effects);
    for (const index of quirks) {
      t.diagnostic(`COPIED GO V1 QUIRK ${name} step ${index}: ${fixture.steps[index - 1].preferred ?? fixture.preferred}`);
    }
  });
}
