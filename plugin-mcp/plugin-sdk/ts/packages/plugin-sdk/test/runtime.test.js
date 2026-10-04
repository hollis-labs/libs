import { test } from 'node:test';
import assert from 'node:assert/strict';
import { once } from 'node:events';
import { setImmediate as nextTurn } from 'node:timers/promises';
import { PassThrough, Writable, Readable } from 'node:stream';
import { createInterface } from 'node:readline';
import { serve, ErrCancelled, PluginError, MAX_INPUT_FRAME_BYTES, TruncatedFrameError } from '../dist/index.js';
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
  r.send(1,'command/execute',{name:'slow',args:'',session_id:''}); await slowStarted.promise;
  r.send(2,'command/execute',{name:'fast',args:'',session_id:''});
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
    r.send(id,'command/execute',{name,args:'',session_id:''}); assert.equal((await r.reply()).error.code,-32603);
  }
  r.send(4,'command/execute',{name:'ok',args:'',session_id:''}); assert.equal((await r.reply()).result.content,'alive');
  r.input.end(); await r.done;
});

test('AbortSignal wakes stdin, cancels pending handler, unload gets fresh signal', {timeout:5000}, async t => {
  const controller = new AbortController(); const started = deferred();
  const p = {...fixturePlugin('base'),async command(ctx) {
    started.resolve(); await once(ctx.signal,'abort'); throw ErrCancelled;
  },unload(ctx) { assert.equal(ctx.signal.aborted,false); }};
  const r = await rig(t,p,{signal:controller.signal});
  r.send(1,'command/execute',{name:'wait',args:'',session_id:''}); await started.promise;
  controller.abort(); assert.equal((await r.reply()).error.code,-32003); await r.done;
  assert.equal(r.input.destroyed,false);assert.equal(r.input.listenerCount('readable'),0);
});

test('identity remains opaque but structural null is invalid', {timeout:5000}, async t => {
  const received = []; const p = {...fixturePlugin('full'),identity(_ctx,v){received.push(v);}};
  const r = await rig(t,p,{initParams:{identity:{subject:'one'}}});
  r.send(2,'command/execute',{name:'echo',args:'',session_id:'',identity:null}); assert.equal((await r.reply()).error.code,-32602);
  r.send(3,'command/execute',{name:'echo',args:'',session_id:'',identity:{MixedCase:1}}); await r.reply();
  assert.deepEqual(received,[{subject:'one'},{MixedCase:1}]); r.input.end(); await r.done;
});

test('required params precede invocation; unsupported capability precedes validation', {timeout:5000}, async t => {
  let calls=0;
  const p = {...fixturePlugin('base'),command(_ctx,req){calls++;assert.equal(req.args,'');return {action:'noop'};}};
  const r = await rig(t,p);
  r.send(1,'command/execute',null); assert.equal((await r.reply()).error.code,-32602);
  r.send(2,'http/handle',[]); assert.equal((await r.reply()).error.code,-32601);
  r.send(3,'command/execute',{name:42}); assert.equal((await r.reply()).error.code,-32602);
  r.send(4,'command/execute',{name:'echo',session_id:'',args:''}); assert.equal((await r.reply()).result.action,'noop');
  assert.equal(calls,1);r.input.end(); await r.done;
});

test('CRLF decodes but final frame without LF is truncated across arbitrary byte chunks', {timeout:5000}, async () => {
  const bytes = Buffer.from('{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\r\n{"jsonrpc":"2.0","id":2,"method":"plugin/health"}');
  let text = ''; const output = new Writable({write(b,_e,cb){text += b.toString();cb();}});
  await assert.rejects(serve(fixturePlugin('base'),{input:Readable.from([...bytes].map(byte=>Buffer.from([byte]))),output}),TruncatedFrameError);
  assert.deepEqual(text.trim().split('\n').map(JSON.parse).map(r=>r.id),[1]);
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

test('explicit unload fences new work, acknowledges once and ends without host EOF', {timeout:5000}, async t => {
  const order = []; const p = {...fixturePlugin('base'),unload(){order.push('unload');},health(){order.push('health');return {ok:true};}};
  const r = await rig(t,p);
  r.input.write('{"jsonrpc":"2.0","id":1,"method":"plugin/unload"}\n{"jsonrpc":"2.0","id":2,"method":"plugin/health"}\n');
  assert.equal((await r.reply()).result.ok,true);
  await r.done;assert.deepEqual(order,['unload']);
  assert.equal(r.input.destroyed,false);assert.equal(r.output.destroyed,false);

});

test('wrapped typed error and cancellation precedence follow Go', {timeout:5000}, async t => {
  const cause = new PluginError(404,'underlying'); cause.cause = ErrCancelled;
  const r = await rig(t,{...fixturePlugin('base'),command(){throw new Error('outer',{cause});}});
  r.send(1,'command/execute',{name:'test',args:'',session_id:''}); assert.deepEqual((await r.reply()).error,{code:-32003,message:'outer'});
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
  r.send(1,'command/execute',{name:'cause',args:'',session_id:''}); assert.equal((await r.reply()).error.code,-32603);
  r.send(2,'command/execute',{name:'unprintable',args:'',session_id:''}); assert.equal((await r.reply()).error.message,'unprintable handler error');
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
test('Init refuses pipelined handlers; optional offers are declined', {timeout:5000}, async t => {
 const started=deferred(),release=deferred();t.after(()=>release.resolve());let handled=false;
 const p={...fixturePlugin('base'),async init(){started.resolve();await release.promise;return {...fixturePlugin('base').init(),hooks_profile_version:1};},load(){handled=true;return {};}};
 const r=await rig(t,p,{initialize:false});
 r.send(1,'plugin/init',initParams({hooks_profile:{hooks_profile_version:1}}));await started.promise;
 r.send(2,'plugin/load');assert.equal((await r.reply()).error.message,'successful init required');assert.equal(handled,false);
 release.resolve();const ack=await r.reply();assert.equal(ack.result.protocol,2);assert.equal(ack.result.hooks_profile_version,undefined);
 r.send(4,'plugin/load');assert.equal((await r.reply()).id,4);assert.equal(handled,true);
 r.send(3,'plugin/init',initParams());assert.equal((await r.reply()).error.code,-32600);
 r.input.end();await r.done;
});

test('lifecycle and health callbacks preserve typed errors and wrapped veto', {timeout:5000}, async t => {
  for (const method of ['plugin/init','plugin/load','plugin/health','plugin/unload']) {
    for (const [error,code] of [[new Error('wrapped',{cause:new PluginError(404,'missing')}),-32000],[new PluginError(409,'conflict'),-32001],[new PluginError(422,'invalid'),-32002],[new Error('wrapped',{cause:ErrCancelled}),-32003],[new Error('failure'),-32603]]) {
      const name = method.slice(7);
      let attempts = 0;
      const plugin = {...fixturePlugin('base'),[name](){attempts++;throw error;}};
      const r = await rig(t,plugin,{initialize:method !== 'plugin/init'});
      r.send(1,method,method === 'plugin/init' ? initParams() : undefined);
      assert.equal((await r.reply()).error.code,code);
      if (method !== 'plugin/unload') r.input.end();
      await r.done;assert.equal(attempts,1);
    }
  }
});

test('authored unhealthy is successful; a thrown health callback is an RPC failure', {timeout:5000}, async t => {
  let throws = false;
  const r = await rig(t,{...fixturePlugin('base'),health(){if(throws) throw 'health panic';return {ok:false,message:'maintenance'};}});
  r.send(1,'plugin/health');assert.deepEqual((await r.reply()).result,{ok:false,message:'maintenance'});
  throws = true;r.send(2,'plugin/health');assert.equal((await r.reply()).error.code,-32603);
  r.input.end();await r.done;
});

test('EOF during explicit cleanup and external abort share one attempt and terminal reply', {timeout:5000}, async t => {
  const controller = new AbortController(),started = deferred(),release = deferred();t.after(()=>release.resolve());
  let attempts = 0;
  const r = await rig(t,{...fixturePlugin('base'),async unload(ctx){attempts++;assert.equal(ctx.signal.aborted,false);started.resolve();await release.promise;}},{signal:controller.signal});
  r.send('terminal','plugin/unload');await started.promise;
  controller.abort();r.input.end();release.resolve();
  assert.deepEqual(await r.reply(),{jsonrpc:'2.0',id:'terminal',result:{ok:true}});
  await r.done;assert.equal(attempts,1);
});

test('cleanup throw and panic are not retried, including EOF failure', {timeout:5000}, async t => {
  for (const explicit of [false,true]) {
    for (const failure of [new PluginError(409,'cleanup failed'),'cleanup panic',null]) {
      let attempts = 0;
      const r = await rig(t,{...fixturePlugin('base'),unload(){attempts++;throw failure;}});
      // Observe rejection immediately, including primitive/null thrown values.
      const result = r.done.then(()=>({ok:true}),error=>({ok:false,error}));
      if(explicit) {r.send(1,'plugin/unload');assert.equal((await r.reply()).error.code,failure instanceof PluginError ? -32001 : -32603);}
      else r.input.end();
      const outcome = await result;assert.equal(outcome.ok,explicit);assert.equal(attempts,1);
    }
  }
});

test('shutdown times out an uncooperative handler without late cleanup or success', {timeout:5000}, async t => {
  const started = deferred(),release = deferred();t.after(()=>release.resolve());let attempts = 0;
  const r = await rig(t,{...fixturePlugin('base'),async command(){started.resolve();await release.promise;return {action:'noop'};},unload(){attempts++;}},{shutdownTimeoutMs:20});
  r.send(1,'command/execute',{name:'test',args:'',session_id:''});await started.promise;
  const result = assert.rejects(r.done,{name:'ShutdownTimeoutError'});r.input.end();await result;
  let late = '';r.output.on('data',chunk=>{late+=chunk;});
  release.resolve();await nextTurn();
  assert.equal(attempts,0);assert.equal(late,'');assert.equal(r.output.destroyed,false);
});

test('shutdown bounds hanging Init, cleanup and blocked output without owning injected streams', {timeout:5000}, async t => {
  for (const phase of ['init','unload','output']) {
    const release = deferred(),started = deferred();t.after(()=>release.resolve());let attempts = 0;
    const plugin = {...fixturePlugin('base'),async init(){if(phase === 'init'){started.resolve();await release.promise;}return fixturePlugin('base').init();},async unload(ctx){attempts++;if(phase === 'unload'){started.resolve();await release.promise;assert.equal(ctx.signal.aborted,true);}}};
    const input = new PassThrough({autoDestroy:false});let pendingWrite;
    const output = new Writable({write(_b,_e,cb){if(phase === 'output'){pendingWrite=cb;started.resolve();}else cb();}});
    t.after(()=>{pendingWrite?.();input.destroy();output.destroy();});
    const done = serve(plugin,{input,output,shutdownTimeoutMs:20});
    const result = assert.rejects(done,{name:'ShutdownTimeoutError'});
    input.end(JSON.stringify({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()})+'\n');
    await started.promise;await result;
    assert.equal(input.destroyed,false);assert.equal(output.destroyed,false);
    release.resolve();pendingWrite?.();pendingWrite=undefined;await nextTurn();
    assert.equal(attempts,phase === 'init' ? 0 : 1);
  }
});

test('unload before successful Init is a terminal refusal with one final cleanup', {timeout:5000}, async t => {
  const plugin = fixturePlugin('lifecycle-shutdown');
  const r = await rig(t,plugin,{initialize:false});
  r.input.write('{"jsonrpc":"2.0","id":1,"method":"plugin/unload"}\n{"jsonrpc":"2.0","id":2,"method":"plugin/health"}\n');
  assert.equal((await r.reply()).error.code,-32600);await r.done;
  assert.deepEqual(plugin.effects(),{unload_attempts:1,health_calls:0});
});

test('forward metadata is per invocation, including Init and terminal cleanup', {timeout:5000}, async t => {
  const seen=[];
  const p={...fixturePlugin('base'),init(ctx){seen.push(ctx.forwardContext);return fixturePlugin('base').init();},load(ctx){seen.push(ctx.forwardContext);return {};},command(ctx){seen.push(ctx.forwardContext);return {action:'noop'};},unload(ctx){seen.push(ctx.forwardContext);}};
  // Fixture-only budgets exercise metadata, not expiration scheduling.
  const r=await rig(t,p,{initParams:{context:{timeout_ms:20000}}});
  r.send(1,'command/execute',{name:'echo',args:'',session_id:'',context:{timeout_ms:10000}});await r.reply();
  r.send(2,'command/execute',{name:'echo',args:'',session_id:''});await r.reply();
  r.send(3,'command/execute',{name:'echo',args:'',session_id:'',context:{timeout_ms:1,parent_call:{}}});assert.equal((await r.reply()).error.code,-32602);
  r.send(4,'plugin/load',{context:{timeout_ms:30000}});await r.reply();
  r.send(5,'plugin/unload',null);assert.equal((await r.reply()).error.code,-32602);
  r.send(6,'plugin/unload',{context:{timeout_ms:40000}});assert.equal((await r.reply()).result.ok,true);await r.done;
  assert.deepEqual(seen,[{timeout_ms:20000},{timeout_ms:10000},undefined,{timeout_ms:30000},{timeout_ms:40000}]);
});

test('required result fields and non-JSON nested values fail without poisoning later calls', {timeout:5000}, async t => {
 const sparse=[];sparse.length=1;
 const values=[{}, {ok:null}, {ok:1}, {ok:true,message:null}, {ok:true,extra:1}];
 const p={...fixturePlugin('full'),health(){return values.shift() ?? {ok:true};},create(){return {nested:undefined};},read(){return {nested:NaN};},update(){return {nested:()=>{}};},list(){return [{nested:sparse}];}};
 const r=await rig(t,p);
 for(let i=0;i<5;i++){r.send(i+1,'plugin/health');assert.equal((await r.reply()).error.code,-32603);}
 for(const method of ['crud/create','crud/read','crud/update','crud/list']){r.send(10,method,{resource_type:'notes',id:'a',data:{},filters:{}});assert.equal((await r.reply()).error.code,-32603);}
 r.send(11,'plugin/health');assert.equal((await r.reply()).result.ok,true);r.input.end();await r.done;
});
