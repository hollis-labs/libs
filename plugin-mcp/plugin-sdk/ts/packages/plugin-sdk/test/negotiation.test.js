import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {PassThrough,Writable} from 'node:stream';
import {createInterface} from 'node:readline';
import {serve} from '../dist/serve.js';
import {fixturePlugin} from './fixtures.js';

const doc=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/negotiation.json',import.meta.url),'utf8'));
function peer(t,plugin,reverseRPC=true,options={},completeWrites=false){
 const input=new PassThrough(),output=new PassThrough(),lines=createInterface({input:output})[Symbol.asyncIterator]();
 // A selected fixture can make whole-write receipt explicit before peer observation.
 if(completeWrites){const write=output.write.bind(output);output.write=(frame,done)=>{const ok=write(frame);done?.();return ok;};}
 const done=serve(plugin,{input,output,reverseRPC,stderr:new Writable({write(_raw,_enc,cb){cb();}}),...options});
 // Observe failures immediately even when a recipe is still awaiting its frame.
 void done.catch(()=>{});t.after(()=>{input.destroy();output.destroy();});
 return {done,send(id,method,params){input.write(JSON.stringify({jsonrpc:'2.0',...(id===undefined?{}:{id}),method,params})+'\n');},reply(id,result){input.write(JSON.stringify({jsonrpc:'2.0',id,result})+'\n');},async read(){const r=await lines.next();assert.equal(r.done,false);return JSON.parse(r.value);},async finish(){input.end();await done;}};
}
for(const v of doc.cases)test('shared reverse negotiation: '+v.name,{timeout:5000},async t=>{
 let observed;
 const base=fixturePlugin('base'),p=peer(t,{...base,init(ctx,input){assert.equal(ctx.host!==undefined,v.init_client);return {...base.init(ctx,input),...(v.authored_ack?{reverse_rpc_version:1}:{})};},health(ctx){observed=ctx.host!==undefined;return {ok:true};}},v.reverseRPC);
 p.send(7,'plugin/init',v.init);const init=await p.read();
 if(v.init_error){assert.equal(init.error.code,-32602);assert.equal(init.error.data.code,v.init_error);await p.finish();return;}
 assert.equal(init.error,undefined);assert.equal(init.result.reverse_rpc_version===1,v.ack);
 p.send(8,'plugin/health',{context:{binding_id:doc.binding_id,timeout_ms:10000}});assert.equal((await p.read()).error,undefined);assert.equal(observed,v.client);await p.finish();
});

test('negotiated lifecycle logs and bound helper use normal Serve',{timeout:5000},async t=>{
 const init=structuredClone(doc.cases[0].init),base=fixturePlugin('base');let saved;
 const log=async ctx=>{assert.ok(ctx.host);saved=ctx.host;await assert.rejects(ctx.host.storageGet({grant_id:'g-StorageGet',key:'k'}),error=>error.code==='target_unavailable');await ctx.host.log({grant_id:'g-Log',level:'info',message:'lifecycle'});};
 const p=peer(t,{...base,async init(ctx,input){await log(ctx);return base.init(ctx,input);},async load(ctx){await log(ctx);return {};},unload:log,async health(ctx){assert.ok(ctx.host);const r=await ctx.host.storageGet({grant_id:'g-StorageGet',key:'k'});return {ok:!r.found};}});
 for(const [i,method] of ['plugin/init','plugin/load','plugin/health','plugin/unload'].entries()){
  const id=7+i;p.send(id,method,i===0?init:{context:{binding_id:doc.binding_id,timeout_ms:10000}});
  const request=await p.read();assert.equal(request.method,method==='plugin/health'?'host/storage/get':'host/log');assert.deepEqual(request.params.context.parent_call,{request_owner:'host',id});assert.ok(request.params.context.timeout_ms>0&&request.params.context.timeout_ms<=1000);
  if(i===0){p.send(17,'plugin/health',{});assert.equal((await p.read()).error.message,'successful init required');}
  p.reply(request.id,method==='plugin/health'?{found:false}:{accepted:true});assert.equal((await p.read()).error,undefined);
 }
 await p.done;await assert.rejects(saved.log({grant_id:'g-Log',level:'info',message:'late'}),error=>error.code==='target_unavailable');
});

test('active connection does not attach a client without a binding or to hooks',{timeout:5000},async t=>{
 const hooks=JSON.parse(readFileSync(new URL('../../../../docs/protocol/v2/transcripts/hooks-negotiated.json',import.meta.url),'utf8'));
 const init=structuredClone(doc.cases[0].init);init.hooks_profile={hooks_profile_version:1};
 const p=peer(t,{...fixturePlugin('base'),health(ctx){assert.equal(ctx.host,undefined);return {ok:true};},hookHandle(ctx,r){assert.equal(ctx.host,undefined);return {invocation_id:r.invocation_id,status:'ok'};}});
 p.send(7,'plugin/init',init);const r=await p.read();assert.equal(r.result.reverse_rpc_version,1);assert.equal(r.result.hooks_profile_version,1);
 p.send(8,'plugin/health',{});assert.equal((await p.read()).error,undefined);
 const call=hooks.steps.find(s=>s.send?.method==='hook/handle').send;p.send(9,call.method,call.params);assert.equal((await p.read()).error,undefined);await p.finish();
});

test('EOF cleanup has no fabricated lifecycle client',{timeout:5000},async t=>{
 let cleaned=false;const p=peer(t,{...fixturePlugin('base'),unload(ctx){assert.equal(ctx.host,undefined);cleaned=true;}});
 p.send(7,'plugin/init',doc.cases[0].init);assert.equal((await p.read()).result.reverse_rpc_version,1);await p.finish();assert.equal(cleaned,true);
});

test('Init snapshot resists author mutation and seeds directional high-water',{timeout:5000},async t=>{
 const base=fixturePlugin('base');let called=false;
 const p=peer(t,{...base,init(ctx,input){input.host_services.methods=[];input.host_services.limits.method_timeout_ms={};input.grants=[];return base.init(ctx,input);},async health(ctx){called=true;assert.ok(ctx.host);await ctx.host.storageGet({grant_id:'g-StorageGet',key:'k'});return {ok:true};}});
 p.send(7,'plugin/init',doc.cases[0].init);assert.equal((await p.read()).result.reverse_rpc_version,1);
 p.send(8,'plugin/health',{context:{binding_id:doc.binding_id,timeout_ms:10000}});const call=await p.read();assert.equal(call.method,'host/storage/get');p.reply(call.id,{found:false});assert.equal((await p.read()).error,undefined);assert.equal(called,true);
 p.send(7,'plugin/health',{});await assert.rejects(p.done,error=>error.name==='CorrelationError');
});

test('narrowed frame and writer policy reject oversize before publication',{timeout:5000},async t=>{
 const init=structuredClone(doc.cases[0].init),l=init.host_services.limits;
 Object.assign(l,{max_frame_bytes:1024,max_queued_write_bytes:2048,host_to_plugin_inflight:1,plugin_to_host_inflight:1,write_timeout_ms:250});l.method_timeout_ms['host/log']=200;
 const base=fixturePlugin('base');
 const p=peer(t,{...base,async init(ctx,input){assert.ok(ctx.host);await assert.rejects(ctx.host.log({grant_id:'g-Log',level:'info',message:'x'.repeat(1500)}),error=>error.name==='FrameTooLargeError');input.host_services.methods=null;input.host_services.limits.method_timeout_ms=null;input.grants=null;return base.init(ctx,input);}});
 p.send(7,'plugin/init',init);assert.equal((await p.read()).result.reverse_rpc_version,1);
 p.send(8,'command/execute',{name:'large',session_id:'',args:'x'.repeat(1100)});await assert.rejects(p.done,error=>error.name==='FrameTooLargeError'&&error.direction==='input');
});

test('failed Init revokes provisional log and late reply cannot revive it',{timeout:5000},async t=>{
 let release;const barrier=new Promise(resolve=>{release=resolve;});let cached,logged;
 const p=peer(t,{...fixturePlugin('base'),async init(ctx){cached=ctx.host;assert.ok(cached);logged=cached.log({grant_id:'g-Log',level:'info',message:'pending'});void logged.catch(()=>{});await barrier;throw new Error('Init failed');}},true,{},true);
 p.send(7,'plugin/init',doc.cases[0].init);const call=await p.read();assert.equal(call.method,'host/log');release();
 let terminal=await p.read();if(terminal.method==='rpc/cancel')terminal=await p.read();assert.equal(terminal.error.code,-32603);await assert.rejects(logged);await assert.rejects(cached.log({grant_id:'g-Log',level:'info',message:'late'}),error=>error.code==='target_unavailable');
 p.reply(call.id,{accepted:true});p.send(8,'plugin/health',{});assert.equal((await p.read()).error.message,'successful init required');await p.finish();
});

for(const identity of [false,true])test('Init terminal failure never activates reverse: '+(identity?'identity deadline':'output fallback'),{timeout:5000},async t=>{
 const init=structuredClone(doc.cases[0].init),base=fixturePlugin('base');
 const plugin={...base,...(identity?{async identity(ctx){if(!ctx.signal.aborted)await new Promise(resolve=>ctx.signal.addEventListener('abort',resolve,{once:true}));}}:{init(ctx,input){return {...base.init(ctx,input),description:'x'.repeat(1500)};}})};
 if(identity){init.identity={};init.context.timeout_ms=50;}else{init.host_services.limits.max_frame_bytes=1024;init.host_services.limits.max_queued_write_bytes=2048;}
 const p=peer(t,plugin);p.send(7,'plugin/init',init);assert.ok((await p.read()).error);
 p.send(8,'plugin/health',{});assert.equal((await p.read()).error.message,'successful init required');await p.finish();
});
