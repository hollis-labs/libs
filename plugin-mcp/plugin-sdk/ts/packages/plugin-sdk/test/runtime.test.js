import { test } from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { setImmediate as nextTurn } from 'node:timers/promises';
import { PassThrough, Writable, Readable } from 'node:stream';
import { createInterface } from 'node:readline';
import { serve, ErrCancelled, PluginError, MAX_INPUT_FRAME_BYTES } from '../dist/index.js';
import { fixturePlugin, initParams } from './fixtures.js';

function deferred() {
  let resolve;
  const promise = new Promise(r => { resolve = r; });
  return { promise, resolve };
}
async function rig(t, plugin, options = {}) {
  const input = new PassThrough(); const output = new PassThrough();
  const lines = createInterface({input:output}); const replies = lines[Symbol.asyncIterator]();
  const done = serve(plugin,{input,output,stderr:new Writable({write(_b,_e,cb){cb();}}),...options});
  t.after(() => { input.destroy(); output.destroy(); lines.close(); });
  const r = {input,output,done,send(id,method,params) { input.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n'); },async reply() { return JSON.parse((await replies.next()).value); }};
  if(options.initialize !== false) {r.send(8000,'plugin/init',initParams(options.initParams)); assert.equal((await r.reply()).result.protocol,2);}
  return r;
}

test('in-flight calls complete out of order; EOF drains before final unload', {timeout:5000}, async t => {
  const slowStarted = deferred(); const release = deferred(); const unloaded = deferred();
  t.after(() => release.resolve());
  const p = {...fixturePlugin('base'),async command(_ctx,r) {
    if(r.name === 'slow') { slowStarted.resolve(); await release.promise; }
    return {action:'message',content:r.name};
  },unload(){unloaded.resolve();}};
  const r = await rig(t,p);
  r.send(1,'command/execute',{name:'slow'}); await slowStarted.promise;
  r.send(2,'command/execute',{name:'fast'});
  assert.deepEqual(await r.reply(),{jsonrpc:'2.0',id:2,result:{action:'message',content:'fast'}});
  const inputEnded = once(r.input,'end');
  r.input.end();
  await inputEnded;
  await nextTurn();
  // Use a behavior barrier rather than sleeps: fast reply arrived while slow
  // remains held, and cleanup must not have run at that point.
  let cleaned = false; unloaded.promise.then(() => {cleaned = true;});
  await Promise.resolve(); assert.equal(cleaned,false);
  release.resolve(); assert.equal((await r.reply()).id,1);
  await r.done; await unloaded.promise;
});

test('sync throw, async rejection and cyclic results are isolated; next call survives', {timeout:5000}, async t => {
  const p = {...fixturePlugin('base'),command(_ctx,r) {
    if(r.name === 'throw') throw new Error('sync failure');
    if(r.name === 'reject') return Promise.reject(new Error('async failure'));
    if(r.name === 'cycle') { const data = {}; data.self = data; return {action:'message',envelopes:[{type:'x',data}]}; }
    return {action:'message',content:'alive'};
  }};
  const r = await rig(t,p);
  for(const [id,name] of [[1,'throw'],[2,'reject'],[3,'cycle']]) {
    r.send(id,'command/execute',{name}); assert.equal((await r.reply()).error.code,-32603);
  }
  r.send(4,'command/execute',{name:'ok'}); assert.equal((await r.reply()).result.content,'alive');
  r.input.end(); await r.done;
});

test('AbortSignal wakes stdin, cancels pending handler, unload gets fresh signal', {timeout:5000}, async t => {
  const controller = new AbortController(); const started = deferred();
  const p = {...fixturePlugin('base'),async command(ctx) {
    started.resolve(); await once(ctx.signal,'abort'); throw ErrCancelled;
  },unload(ctx) { assert.equal(ctx.signal.aborted,false); }};
  const r = await rig(t,p,{signal:controller.signal});
  r.send(1,'command/execute',{name:'wait'}); await started.promise;
  controller.abort(); assert.equal((await r.reply()).error.code,-32003); await r.done;
});

test('identity is opaque, per call; null is present and omitted is not delivered', {timeout:5000}, async t => {
  const received = []; const p = {...fixturePlugin('full'),identity(_ctx,v){received.push(v);}};
  const r = await rig(t,p,{initParams:{identity:{subject:'one'}}});
  r.send(2,'command/execute',{name:'echo',identity:null}); await r.reply();
  r.send(3,'command/execute',{name:'echo'}); await r.reply();
  assert.deepEqual(received,[{subject:'one'},null]); r.input.end(); await r.done;
});

test('omitted/null fields get Go zero values; unsupported capability precedes params validation', {timeout:5000}, async t => {
  const p = {...fixturePlugin('base'),command(_ctx,req){assert.equal(req.name,'');assert.equal(req.args,'');return {action:'noop'};}};
  const r = await rig(t,p);
  r.send(1,'command/execute',null); assert.equal((await r.reply()).result.action,'noop');
  r.send(2,'http/handle',[]); assert.equal((await r.reply()).error.code,-32601);
  r.send(3,'command/execute',{name:42}); assert.equal((await r.reply()).error.code,-32602);
  r.input.end(); await r.done;
});

test('CRLF and final frame without LF decode across arbitrary byte chunks', {timeout:5000}, async () => {
  const bytes = Buffer.from('{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\r\n{"jsonrpc":"2.0","id":2,"method":"plugin/health"}');
  let text = ''; const output = new Writable({write(b,_e,cb){text += b.toString();cb();}});
  await serve(fixturePlugin('base'),{input:Readable.from([...bytes].map(byte=>Buffer.from([byte]))),output});
  assert.deepEqual(text.trim().split('\n').map(JSON.parse).map(r=>r.id).sort(),[1,2]);
});

test('frame cap counts UTF-8 bytes rather than JS characters', {timeout:5000}, async () => {
  const output = new Writable({write(_b,_e,cb){cb();}});
  await assert.rejects(serve(fixturePlugin('base'),{input:Readable.from(['é'.repeat(MAX_INPUT_FRAME_BYTES / 2)]),output}),{name:'FrameTooLargeError'});
});

test('output errors reject serve after unload without an unhandled rejection', {timeout:5000}, async () => {
  let unloaded = false;
  const output = new Writable({write(_b,_e,cb){cb(new Error('output failed'));}});
  await assert.rejects(serve({...fixturePlugin('base'),unload(){unloaded=true;}},{input:Readable.from(['{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\n']),output}),/output failed/);
  assert.equal(unloaded,true);
});

test('explicit unload acknowledges without ending loop, then EOF cleanup follows', {timeout:5000}, async t => {
  const order = []; const p = {...fixturePlugin('base'),unload(){order.push('unload');},health(){order.push('health');return {ok:true};}};
  const r = await rig(t,p); r.send(1,'plugin/unload'); assert.equal((await r.reply()).result.ok,true);
  r.send(2,'plugin/health'); await r.reply(); r.input.end(); await r.done;
  assert.deepEqual(order,['unload','health','unload']);
});

test('wrapped typed error and cancellation precedence follow Go', {timeout:5000}, async t => {
  const cause = new PluginError(404,'underlying'); cause.cause = ErrCancelled;
  const r = await rig(t,{...fixturePlugin('base'),command(){throw new Error('outer',{cause});}});
  r.send(1,'command/execute'); assert.deepEqual((await r.reply()).error,{code:-32003,message:'outer'});
  r.input.end(); await r.done;
});

test('invalid envelopes never invoke handlers; IDs retain presence and exact values', {timeout:5000}, async t => {
  let calls = 0;
  const r = await rig(t,{...fixturePlugin('base'),health(){calls++;return {ok:true};}});
  for (const token of ['null','9007199254740993','-9007199254740992','1e0','1.0','true','[]','{}']) {
    r.input.write(`{"jsonrpc":"2.0","id":${token},"method":"plugin/health"}\n`);
    assert.deepEqual(await r.reply(),{jsonrpc:'2.0',id:null,error:{code:-32600,message:'invalid request'}});
  }
  r.input.write('{"jsonrpc":"2.0","id":1,"id":2,"method":"plugin/health"}\n');
  assert.equal((await r.reply()).id,null);
  r.input.write('{"jsonrpc":"2.0","id":"recover","method":1,"method":"plugin/health"}\n');
  assert.equal((await r.reply()).id,'recover');
  assert.equal(calls,0);
  for (const id of ['',0,-1,9007199254740991,-9007199254740991,'text']) {
    r.send(id,'plugin/health');assert.equal((await r.reply()).id,id);
  }
  r.input.write('{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}\n');
  r.input.write('{"jsonrpc":"2.0","id":"unsolicited","result":null}\n');
  r.send(undefined,'plugin/health');
  r.send('barrier','plugin/health');assert.equal((await r.reply()).id,'barrier');
  r.input.end();await r.done;assert.equal(calls,8);
});

test('exception stringification and cause getters cannot escape handler isolation', {timeout:5000}, async t => {
  const r = await rig(t,{...fixturePlugin('base'),command(_ctx,req) {
    if(req.name === 'cause') { const error = new Error('outer'); Object.defineProperty(error,'cause',{get(){throw new Error('getter');}}); throw error; }
    throw {toString(){throw new Error('stringifier');}};
  }});
  r.send(1,'command/execute',{name:'cause'}); assert.equal((await r.reply()).error.code,-32603);
  r.send(2,'command/execute',{name:'unprintable'}); assert.equal((await r.reply()).error.message,'unprintable handler error');
  r.send(3,'plugin/health'); assert.equal((await r.reply()).result.ok,true);
  r.input.end(); await r.done;
});

test('Init validation and lifecycle ordering precede plugin code', {timeout:5000}, async t => {
 let calls=0;
 const p={...fixturePlugin('base'),init(){calls++;return fixturePlugin('base').init();}};
 const r=await rig(t,p,{initialize:false});
 r.send(1,'plugin/load');assert.equal((await r.reply()).error.code,-32600);
 r.send(2,'plugin/init',{});const failure=(await r.reply()).error;
 assert.equal(failure.code,-32602);assert.equal(failure.data.contract,'plugin-init/2');assert.equal(failure.data.code,'invalid_init');
 r.send(3,'plugin/load');assert.equal((await r.reply()).error.code,-32600);
 r.send(4,'plugin/init',initParams());assert.equal((await r.reply()).error.code,-32600);
 assert.equal(calls,0);r.input.end();await r.done;
});
test('Init is an admission barrier for pipelined handlers; optional offers are declined', {timeout:5000}, async t => {
 const started=deferred(),release=deferred();t.after(()=>release.resolve());let handled=false;
 const p={...fixturePlugin('base'),async init(){started.resolve();await release.promise;return {...fixturePlugin('base').init(),hooks_profile_version:1};},load(){handled=true;return {};}};
 const r=await rig(t,p,{initialize:false});
 r.send(1,'plugin/init',initParams({hooks_profile:{hooks_profile_version:1}}));await started.promise;
 r.send(2,'plugin/load');await nextTurn();assert.equal(handled,false);
 release.resolve();const ack=await r.reply();assert.equal(ack.result.protocol,2);assert.equal(ack.result.hooks_profile_version,undefined);
 assert.equal((await r.reply()).id,2);assert.equal(handled,true);
 r.send(3,'plugin/init',initParams());assert.equal((await r.reply()).error.code,-32600);
 r.input.end();await r.done;
});
