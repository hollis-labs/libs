import { test } from 'node:test';
import assert from 'node:assert/strict';
import { access, readFile, writeFile } from 'node:fs/promises';
import { ConfigReader, SecretTracker, createLogger, hasCapability, resolvedDataDir } from '../dist/index.js';
import { createHarness } from '../dist/harness.js';
import { fixturePlugin } from './fixtures.js';

test('config uses resolved host values, Go bool spelling, safe integers and explicit required fields', () => {
  const c = new ConfigReader({truth:'True',falsehood:'FALSE',invalid:'yes',number:'-12',junk:'12abc',overflow:'9007199254740992',space:' ',empty:''});
  assert.equal(c.bool('truth'),true); assert.equal(c.bool('falsehood'),false); assert.equal(c.bool('invalid'),false);
  assert.equal(c.int('number'),-12); assert.equal(c.int('junk'),0); assert.equal(c.int('overflow'),0);
  assert.equal(c.has('empty'),true); assert.equal(c.has('toString'),false); assert.equal(c.string('toString'),'');
  assert.equal(c.required('space'),' '); assert.throws(() => c.required('empty'),/missing or empty/);
  assert.equal(new ConfigReader(null).string('missing'),'');
  assert.equal(hasCapability({grants:[{name:'storage.read'}]},'storage.read'),true);
  assert.equal(hasCapability({},'storage.read'),false); assert.throws(() => resolvedDataDir({}),/not set by host/);
});

test('config.secret feeds parent/child loggers; nested text and unserializable fallback redact', () => {
  const secrets = new SecretTracker(); const lines = [];
  const c = new ConfigReader({token:'fixture-token'},secrets);
  const logger = createLogger({secrets,write:line=>lines.push(line)});
  const child = logger.with({request:'one',token:'fixture-token'});
  c.secret('token');
  child.info('contains fixture-token',{nested:{value:'Bearer fixture-token'}});
  const cycle = {}; cycle.self = cycle;
  child.error('failure fixture-token',{cycle});
  logger.warn('safe',{token:'fixture-token'});
  assert.ok(lines.every(line=>!line.includes('fixture-token')));
  assert.equal(JSON.parse(lines[0]).token,'[REDACTED]');
  assert.equal(JSON.parse(lines[0]).nested.value,'Bearer [REDACTED]');
  assert.equal(JSON.parse(lines[1]).marshal_error,'unserializable log fields');
});

test('each runtime/harness secret tracker is independent', () => {
  const a = []; const b = []; const tracker = new SecretTracker(); tracker.add('one-secret');
  createLogger({secrets:tracker,write:line=>a.push(line)}).info('one-secret');
  createLogger({write:line=>b.push(line)}).info('one-secret');
  assert.equal(JSON.parse(a[0]).msg,'[REDACTED]'); assert.equal(JSON.parse(b[0]).msg,'one-secret');
});

test('harness direct mode preserves objects, optional JSON mode clones and rejects impossible wire values', async t => {
  const seen = [];
  const p = {...fixturePlugin('full'),command(_ctx,r){seen.push(r.identity);return {action:'noop',envelopes:[{type:'x',data:{value:r.identity}}]};}};
  const direct = await createHarness(p,{jsonRoundtrip:false});
  const wire = await createHarness(p,{jsonRoundtrip:true,grants:[],config:{key:'value'}});
  t.after(()=>direct.close()); t.after(()=>wire.close());
  assert.equal((await wire.init()).protocol,2); assert.equal((await wire.load()).skipped_registrations[0].id,'optional');
  const identity = {subject:'one'};
  await direct.command('echo','s','',identity); assert.equal(seen[0],identity);
  const result = await wire.command('echo','s','',identity);
  assert.deepEqual(seen[1],identity); assert.notEqual(seen[1],identity); assert.notEqual(result.envelopes[0].data.value,identity);
  await assert.rejects(wire.command('echo','s','',{callback(){}}),/unserializable/);
  await assert.rejects(wire.command('echo','s','',{huge:1n}),/BigInt/);
  await writeFile(`${wire.dataDir}/fixture.txt`,'data'); assert.equal(await readFile(`${wire.dataDir}/fixture.txt`,'utf8'),'data');
  await wire.close(); await wire.close(); await assert.rejects(access(wire.pluginDir),{code:'ENOENT'});
});

test('harness covers event/CRUD/MCP/HTTP/migration/health and unload', async t => {
  const h = await createHarness(fixturePlugin('full'),{jsonRoundtrip:true}); t.after(()=>h.close());
  assert.deepEqual(await h.event({type:'check',source:'host',data:{},pre_hook:true}),{cancel:true,reason:'veto'});
  assert.deepEqual(await h.create('x',{text:'one'}),{text:'one'});
  assert.deepEqual(await h.read('x','id'),{id:'id',resource_type:'x'});
  assert.deepEqual(await h.update('x','id',{text:'two'}),{text:'two'}); await h.delete('x','id'); assert.equal(await h.list('x'),null);
  assert.deepEqual((await h.mcp({tool_name:'echo',arguments:{x:1}})).content,{x:1});
  const response = await h.http({method:'POST',path:'/echo',body:Uint8Array.from([0,255,128])});
  assert.deepEqual([...response.body],[0,255,128]); await h.migrate('1','2'); assert.deepEqual(await h.health(),{ok:true}); await h.unload();
});

test('harness missing capability errors and default health match direct Go harness', async t => {
  const h = await createHarness(fixturePlugin('base')); t.after(()=>h.close());
  assert.deepEqual(await h.health(),{ok:true}); await assert.rejects(h.command('x'),/CommandHandler/); await assert.rejects(h.migrate('1','2'),/Migrator/);
});
