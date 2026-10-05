// Real negotiated stdio children, driven by the same bounded and reaping parent.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {ChildSession} from './child-parent.js';
import {childSpec} from './child-spec.js';
import {replayCases} from './child-cases-replay.js';
import {parseJSONTokens,inspectEnvelope} from '../dist/strict-json.js';
import {decodeHostRPCDTO} from '../dist/host-rpc.js';
const doc=parseJSONTokens(await readFile(new URL('../../../../protocol/v2/fixtures/negotiation.json',import.meta.url),'utf8'));
const template=doc.cases[0].init;
let activeCase;
async function start(runtime,child,mode,signal){
 const spec=childSpec(runtime,'negotiated:'+mode,child);
 return ChildSession.start(spec.command,spec.args,{...spec.options,signal});
}
async function send(s,id,method,params={}){await s.send(JSON.stringify({jsonrpc:'2.0',id,method,params}));}
async function response(s,id){return (await s.frame(v=>!v.method&&v.id===id)).value;}
async function finish(s,success=true,cancelId){
 await s.end();const finished=await s.event(e=>e.kind==='finished');
 assert.deepEqual(await s.wait(),{code:success?0:1,signal:null});
 assert.equal(finished.effects.unload_attempts,1);
 if(cancelId!==undefined)while(s.frames.items.length){const cancel=(await s.frame()).value;assert.equal(cancel.method,'rpc/cancel');assert.deepEqual(cancel.params,{request_owner:'plugin',id:cancelId,reason:'parent_cancelled'});}
 assert.equal(s.frames.items.length,0,'unexpected late output');if(s.failure)throw s.failure;
 return finished;
}
async function request(s,method,parent,ceiling=1000,binding='binding-example'){
 const frame=await s.frame(v=>v.method===method),v=frame.value;
 const dto=method==='host/log'?'LogParams':'StorageGetParams';
 const params=decodeHostRPCDTO(dto,inspectEnvelope(frame.raw).fields.get('params'));
 assert.deepEqual(params.context.parent_call,{request_owner:'host',id:parent});
 assert.equal(params.context.binding_id,binding);
 assert.ok(params.context.timeout_ms>0&&params.context.timeout_ms<=ceiling);
 assert.ok(Number.isSafeInteger(v.id)&&v.id>0);return v;
}
async function reply(s,call,result){await s.send(JSON.stringify({jsonrpc:'2.0',id:call.id,result}));}
const bound={context:{binding_id:'binding-example',timeout_ms:10000}};
export async function replayNegotiated(runtime,child,names,signal){
 const results=[];
 async function run(name,mode,body){
  if(names&&!names.includes(name))return;activeCase=name;const s=await start(runtime,child,mode,signal);
  try{await body(s);results.push({case:name,status:'passed',mode:'normal-serve-negotiated'});}
  catch(e){e.fixtureCase=name;throw e;}
  finally{await s.dispose();assert.ok(s.exit,'negotiated child not reaped');}
 }
 for(const vector of doc.cases){
  const mode=vector.reverseRPC?(vector.authored_ack?'observe-authored':'observe'):(vector.authored_ack?'authored-only':'no-opt-in');
  await run('negotiation:'+vector.name,mode,async s=>{
   await send(s,7,'plugin/init',vector.init);const r=await response(s,7);
   if(vector.init_error){assert.equal(r.error.code,-32602);assert.equal(r.error.data.code,vector.init_error);await finish(s);return;}
   assert.equal(r.error,undefined);assert.equal(r.result.reverse_rpc_version,vector.ack?1:undefined);
   assert.equal((await s.event(e=>e.kind==='init_client')).client,vector.init_client);
   await send(s,8,'plugin/health',bound);await response(s,8);
   assert.equal((await s.event(e=>e.kind==='health_client')).client,vector.client);
   if(!vector.ack){await send(s,'base','plugin/health',{});assert.equal((await response(s,'base')).result.ok,true);await send(s,'base','plugin/health',{});await response(s,'base');}
   await finish(s);
  });
 }
 // Invalid raw tokens must survive the real parent pipe without JSON.parse normalization.
 const invalid=[
  ['null-offer',v=>{v.host_services=null;}],
  ['zero-ceiling',v=>{v.host_services.limits.method_timeout_ms['host/log']=0;}],
  ['unknown-offer-field',v=>{v.host_services.extra=1;}],
  ['null-methods',v=>{v.host_services.methods=null;}],
  ['missing-limit',v=>{delete v.host_services.limits.max_depth;}],
  ['duplicate-offer',null],['case-offer',null],
 ];
 for(const [name,mutate]of invalid)await run('invalid:'+name,'observe',async s=>{
  const init=structuredClone(template);mutate?.(init);
  let raw=JSON.stringify({jsonrpc:'2.0',id:7,method:'plugin/init',params:init});
  if(name==='duplicate-offer')raw=raw.replace('"reverse_rpc_version":1','"reverse_rpc_version":1,"reverse_rpc_version":1');
  if(name==='case-offer')raw=raw.replace('"reverse_rpc_version":1','"Reverse_rpc_version":1');
  await s.send(raw);const r=await response(s,7);assert.equal(r.error.code,-32602);assert.equal(r.error.data.contract,'plugin-init/2');await finish(s);
 });
 for(const mode of ['get','mutate','authored-ack'])await run('bound-helper:'+mode,mode,async s=>{
  await send(s,7,'plugin/init',template);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  await send(s,8,'plugin/health',bound);const call=await request(s,'host/storage/get',8);
  await reply(s,call,{found:false});assert.equal((await response(s,8)).result.ok,true);
  await s.release('probe-cached');assert.equal((await s.event(e=>e.kind==='cached_probe')).failure.code,'target_unavailable');
  await send(s,9,'plugin/health',{});assert.equal((await response(s,9)).result.ok,false);
  await finish(s);
 });
 for(const detail of ['parent_invalid','parent_terminal','stale_binding'])await run('typed-refusal:'+detail,'get',async s=>{
  await send(s,7,'plugin/init',template);await response(s,7);await send(s,8,'plugin/health',bound);
  const call=await request(s,'host/storage/get',8),data={contract:'host-rpc/1',code:'target_unavailable',request_id:call.id,effect_state:'not_started',retryable:false,detail};
  decodeHostRPCDTO('HostRPCErrorData',JSON.stringify(data));
  await s.send(JSON.stringify({jsonrpc:'2.0',id:call.id,error:{code:-32010,message:'typed fixture refusal',data}}));
  assert.equal((await response(s,8)).result.ok,false);
  const result=await s.event(e=>e.kind==='health_helper');assert.equal(result.failure.code,'target_unavailable');assert.equal(result.failure.detail,detail);await finish(s);
 });
 for(const reverse of [false,true])await run('independent-hooks:'+reverse,reverse?'observe':'no-opt-in',async s=>{
  const init=structuredClone(template);init.hooks_profile={hooks_profile_version:1};
  await send(s,7,'plugin/init',init);const r=await response(s,7);assert.equal(r.result.hooks_profile_version,1);assert.equal(r.result.reverse_rpc_version,reverse?1:undefined);
  const hook=parseJSONTokens(await readFile(new URL('../../../../docs/protocol/v2/transcripts/hooks-negotiated.json',import.meta.url),'utf8')).steps.find(step=>step.send?.method==='hook/handle').send;
  await send(s,8,'hook/handle',hook.params);assert.equal((await response(s,8)).result.status,'ok');assert.equal((await s.event(e=>e.kind==='hook_client')).client,false);await finish(s);
 });
 await run('unoffered-helper','subset',async s=>{
  const init=structuredClone(template);init.host_services.methods=['host/log'];init.host_services.limits.method_timeout_ms={'host/log':1000};
  await send(s,7,'plugin/init',init);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  await send(s,8,'plugin/health',bound);assert.equal((await response(s,8)).result.ok,false);
  assert.equal((await s.event(e=>e.kind==='health_helper')).failure.code,'unsupported_capability');await finish(s);
 });
 await run('lifecycle-directional-id-collision','lifecycle',async s=>{
  await send(s,1,'plugin/init',template);const initLog=await request(s,'host/log',1,1000,'lifecycle-binding');assert.equal(initLog.id,1);
  assert.equal((await s.event(e=>e.kind==='lifecycle_business')).failure.code,'target_unavailable');
  await send(s,2,'plugin/health',bound);assert.equal((await response(s,2)).error.message,'successful init required');
  await reply(s,initLog,{accepted:true});assert.equal((await response(s,1)).result.reverse_rpc_version,1);
  for(const [id,method]of [[3,'plugin/load'],[4,'plugin/unload']]){
   await send(s,id,method,bound);const call=await request(s,'host/log',id);assert.ok(call.id>initLog.id);
   assert.equal((await s.event(e=>e.kind==='lifecycle_business')).failure.code,'target_unavailable');
   await reply(s,call,{accepted:true});assert.equal((await response(s,id)).error,undefined);
  }
  const finished=await s.event(e=>e.kind==='finished');assert.equal(finished.effects.unload_attempts,1);assert.deepEqual(await s.wait(),{code:0,signal:null});assert.equal(s.frames.items.length,0);
 });
 for(const mode of ['init-error','init-panic','fallback','wait-init'])await run('failed-init:'+mode,mode,async s=>{
  const init=structuredClone(template);
  if(mode==='fallback'){init.host_services.limits.max_frame_bytes=2048;init.host_services.limits.max_queued_write_bytes=4096;}
  if(mode==='wait-init')init.context.timeout_ms=150;
  await send(s,7,'plugin/init',init);assert.ok((await response(s,7)).error);
  await s.release('probe-cached');assert.equal((await s.event(e=>e.kind==='cached_probe')).failure.code,'target_unavailable');
  await send(s,8,'plugin/health',bound);assert.equal((await response(s,8)).error.message,'successful init required');await finish(s);
 });
 await run('failed-init:cancel','wait-init',async s=>{
  await send(s,7,'plugin/init',template);assert.equal((await s.event(e=>e.kind==='init_client')).client,true);
  await s.send(JSON.stringify({jsonrpc:'2.0',method:'rpc/cancel',params:{request_owner:'host',id:7,reason:'caller_cancelled'}}));
  assert.ok((await response(s,7)).error);await s.release('probe-cached');assert.equal((await s.event(e=>e.kind==='cached_probe')).failure.code,'target_unavailable');
  await send(s,8,'plugin/health',bound);assert.equal((await response(s,8)).error.message,'successful init required');await finish(s);
 });
 await run('failed-init-pending-log-late-reply','pending-init',async s=>{
  await send(s,7,'plugin/init',template);const log=await request(s,'host/log',7,1000,'lifecycle-binding');
  await s.release('fail-init');assert.ok((await response(s,7)).error);assert.notEqual((await s.event(e=>e.kind==='pending_done')).failure.code,'ok');
  await s.release('probe-cached');assert.equal((await s.event(e=>e.kind==='cached_probe')).failure.code,'target_unavailable');
  await reply(s,log,{accepted:true});await send(s,8,'plugin/health',bound);assert.equal((await response(s,8)).error.message,'successful init required');
  // Cancellation notifications are allowed after the failed provisional call, never a helper retry.
  await finish(s,true,log.id);
 });
 for(const end of ['eof','signal'])await run('cleanup-without-parent:'+end,'observe',async s=>{
  await send(s,7,'plugin/init',template);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  if(end==='signal')s.signal('SIGTERM');else await s.end();
  assert.equal((await s.event(e=>e.kind==='cleanup_client')).client,false);
  const finished=await s.event(e=>e.kind==='finished');assert.equal(finished.effects.unload_attempts,1);assert.deepEqual(await s.wait(),{code:0,signal:null});assert.equal(s.frames.items.length,0);
 });
 await run('accepted-narrowed-admission','expanded',async s=>{
  const init=structuredClone(template);Object.assign(init.host_services.limits,{host_to_plugin_inflight:1,plugin_to_host_inflight:1});
  await send(s,7,'plugin/init',init);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  const command=(id,name,args)=>send(s,id,'command/execute',{name,args:JSON.stringify(args),session_id:'',...bound});
  await command(8,'hold',{});await s.event(e=>e.kind==='entered'&&e.id===8);
  await command(9,'hold',{});const refusal=await response(s,9);assert.equal(refusal.error.data.code,'rate_limited');assert.equal(refusal.error.data.effect_state,'not_started');
  await s.release('snapshot');assert.equal((await s.event(e=>e.kind==='snapshot')).effects.hold,1);
  await s.release('request-8');await response(s,8);
  await command(10,'get',{n:2,key:'read'});const call=await request(s,'host/storage/get',10);
  assert.equal((await s.event(e=>e.kind==='helper_done'&&e.failure.code==='rate_limited')).failure.effect_state,'not_started');
  await reply(s,call,{found:false});const result=JSON.parse((await response(s,10)).result.content);
  assert.equal(result.filter(r=>r.code==='ok').length,1);assert.equal(result.filter(r=>r.code==='rate_limited').length,1);await finish(s);
 });
 for(const [mode,offer,expected] of [
  ['policy',{host_to_plugin_inflight:1,plugin_to_host_inflight:1,control_slots:2,max_frame_bytes:1024,max_queued_write_bytes:8192,write_timeout_ms:250},{forward:1,reverse:1,control:2,frame:1024,queue:8192,write_ms:250}],
  ['local-limits',{host_to_plugin_inflight:100,plugin_to_host_inflight:100,control_slots:100,max_frame_bytes:16384,max_queued_write_bytes:32768,write_timeout_ms:2000},{forward:16,reverse:8,control:2,frame:4096,queue:8192,write_ms:300}],
 ])await run('accepted-minima:'+mode,mode,async s=>{
  const init=structuredClone(template);Object.assign(init.host_services.limits,offer);
  await send(s,7,'plugin/init',init);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  const policy=await s.event(e=>e.kind==='policy');assert.deepEqual(policy,{kind:'policy',...expected});
  await send(s,8,'command/execute',{name:'overflow',session_id:'',args:'{}'});const output=await response(s,8);
  if(mode==='policy'){assert.equal(output.error.data.code,'budget_exceeded');assert.equal(output.error.data.effect_state,'committed');}else assert.equal(output.result.action,'message');
  // LF-inclusive input bound, applied to subsequent real pipe reads.
  await send(s,9,'command/execute',{name:'hold',session_id:'',args:'x'.repeat(5000)});
  const finished=await s.event(e=>e.kind==='finished');assert.ok(finished.transport_error.includes('FrameTooLargeError'));assert.equal((await s.wait()).code,1);
 });
 await run('offered-physical-write-timeout','expanded',async s=>{
  const init=structuredClone(template);init.host_services.limits.write_timeout_ms=250;
  await send(s,7,'plugin/init',init);assert.equal((await response(s,7)).result.reverse_rpc_version,1);
  await s.release('arm-writer');await send(s,8,'plugin/health',bound);await s.event(e=>e.kind==='writer_waiting');
  const finished=await s.event(e=>e.kind==='finished');assert.ok(finished.transport_error);assert.equal(finished.effects.unload_attempts,1);assert.deepEqual(await s.wait(),{code:1,signal:null});assert.equal(s.frames.items.length,0);
 });
 await run('init-id-high-water','observe',async s=>{
  await send(s,7,'plugin/init',template);await response(s,7);await send(s,7,'plugin/health',bound);
  const finished=await s.event(e=>e.kind==='finished');assert.ok(finished.transport_error);assert.equal((await s.wait()).code,1);assert.equal(s.frames.items.length,0);
 });
 return results;
}
if(process.argv[1]===fileURLToPath(import.meta.url)){
 const [runtime,child,...names]=process.argv.slice(2),controller=new AbortController();
 const abort=()=>controller.abort();process.once('SIGTERM',abort);process.once('SIGINT',abort);
 try{
  const results=await replayNegotiated(runtime,child,names.length?names:undefined,controller.signal);
  const expanded=names.length?[]:await replayCases(runtime,child,undefined,controller.signal,true);
  for(const r of [...results,...expanded])console.log(JSON.stringify({runtime,...r}));
  console.log(`${runtime} negotiated child PASS (${results.length} negotiation/lifecycle, ${expanded.filter(r=>r.status==='passed').length} expanded, ${expanded.filter(r=>r.status==='unavailable').length} unavailable)`);
 }catch(e){console.error(JSON.stringify({runtime,case:e.fixtureCase??activeCase,status:'failed',failure:e.code??e.message}));process.exitCode=1;}
 finally{process.removeListener('SIGTERM',abort);process.removeListener('SIGINT',abort);}
}
