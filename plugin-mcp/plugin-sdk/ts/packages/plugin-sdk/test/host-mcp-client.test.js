import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Writable} from 'node:stream';
import {Admission} from '../dist/admission.js';
import {Correlation} from '../dist/correlation.js';
import {FrameWriter} from '../dist/publication.js';
import {encodeBoundedJSON} from '../dist/frame-codec.js';
import {hostClientContext,HostRPCFailure} from '../dist/host-client.js';
import {SecretTracker} from '../dist/log.js';
const documents=['readonly','mcp','bindings'].map(name=>JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/host-'+name+'.json',import.meta.url),'utf8')));
function fixture(t,init=documents[0].init){
 const output=new Writable({write(_raw,_enc,done){done();}}),secrets=new SecretTracker();
 const writer=new FrameWriter(output,1000,error=>assert.fail(error));
 const core=new Correlation(true);core.methodTimeoutMS={...init.host_services.limits.method_timeout_ms};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';
 const admission=new Admission(writer,core,core.encode,error=>assert.fail(error));
 const context={signal:new AbortController().signal,logger:{warn(){},debug(){},info(){},error(){}},config:{}};
 core.admit(17);const scope=admission.begin(context,{jsonrpc:'2.0',id:17,method:'plugin/health',params:{context:{binding_id:'binding-example',timeout_ms:10000}}},performance.now());assert.ok(scope);
 const ctx=hostClientContext(scope.ctx,core,init,secrets);
 t.after(async()=>{scope.reply({jsonrpc:'2.0',id:17,result:{ok:true}});await scope.terminalDone;scope.finish();await scope.executionDone;await writer.flush();core.close();writer.abort(new Error('closed'));output.destroy();});
 return {ctx,client:ctx.host,core,scope};
}
const toolArgs={grant_id:'g-MCPCallTool',server_id:'example.server',tool_name:'lookup',tool_binding:'tool-binding-example',arguments:{}};
for(const doc of documents)for(const v of doc.cases)test('shared 3i helper: '+v.name,async t=>{
 const f=fixture(t,doc.init);let calls=0,seed;
 f.core.publish=frame=>{
  const req=JSON.parse(frame);if(v.helper==='mcpCancelCall'&&req.method==='host/mcp/call_tool'){seed=req;return Promise.resolve();}
  calls++;assert.equal(req.method,v.method);const{context,...args}=req.params;assert.deepEqual(args,v.args);assert.equal(context.binding_id,'binding-example');assert.deepEqual(context.parent_call,{request_owner:'host',id:17});assert.ok(context.timeout_ms>0&&context.timeout_ms<=1000);
  f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,...(v.error?{error:v.error}:{result:v.result})}));
  if(seed)f.core.reply(JSON.stringify({jsonrpc:'2.0',id:seed.id,result:{content:[],is_error:false}}));return Promise.resolve();
 };
 let args=v.args,ref;
 if(v.helper==='mcpCancelCall'){ref=f.client.mcpCallTool(toolArgs);args={grant_id:v.args.grant_id,call:ref};}
 const invoke=()=>{const result=f.client[v.helper](args);return v.helper==='mcpCallTool'?result.result:result;};
 if(v.expected_error?.startsWith('host:')){await assert.rejects(invoke(),error=>error instanceof HostRPCFailure&&error.data.code===v.error.data.code&&error.data.detail===v.error.data.detail);return;}
 if(v.expected_error){await assert.rejects(invoke(),error=>error.code===v.expected_error);if(v.expected_error==='capability_denied')assert.equal(calls,0);return;}
 const result=await invoke();assert.equal(calls,1);assert.deepEqual(result,v.result);if(ref)await ref.result;
});
test('same-table cancellation accepts only scoped SDK handles and awaits the tool outcome',async t=>{
 const f=fixture(t),frames=[];f.core.publish=frame=>{frames.push(JSON.parse(frame));return Promise.resolve();};
 const call=f.client.mcpCallTool(toolArgs);const target=frames[0];assert.ok(target);assert.deepEqual(Object.keys(call),['result']);assert.equal(Object.isFrozen(call),true);
 await assert.rejects(f.client.mcpCancelCall({grant_id:'g-MCPCancelCall',call:{result:Promise.resolve({})}}),error=>error.code==='scope_denied');
 const foreign=fixture(t);await assert.rejects(foreign.client.mcpCancelCall({grant_id:'g-MCPCancelCall',call}),error=>error.code==='scope_denied');
 const cancel=f.client.mcpCancelCall({grant_id:'g-MCPCancelCall',call}),request=frames[1];assert.equal(request.params.target_call_id,target.id);assert.notEqual(request.id,target.id);
 f.core.reply(JSON.stringify({jsonrpc:'2.0',id:request.id,result:{accepted:true,already_terminal:false}}));await cancel;
 let settled=false;call.result.then(()=>{settled=true;});await Promise.resolve();assert.equal(settled,false);
 f.core.reply(JSON.stringify({jsonrpc:'2.0',id:target.id,result:{content:[],is_error:true}}));assert.equal((await call.result).is_error,true);
});
test('renewal excludes overlap and installs receipt-anchored skew-capped scope metadata',async t=>{
 const f=fixture(t),frames=[];f.core.publish=frame=>{frames.push(JSON.parse(frame));return Promise.resolve();};
 const call=f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:250});const req=frames[0];
 await assert.rejects(f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:250}),error=>error.code==='rate_limited'&&error.effect_state==='not_started');assert.equal(frames.length,1);
 const result={binding_id:'binding-example',expires_at:'2099-01-01T00:00:00Z',remaining_budgets:{timeout_ms:1000,bytes:0}};
 const receipt=performance.now();f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,result}));await call;
 assert.equal(f.scope.lease.renewPending,false);assert.ok(f.scope.lease.end>=receipt);assert.ok(f.scope.lease.end<=performance.now()+250);const budget=f.scope.lease.budgetEnd;
 const get=f.client.storageGet({grant_id:'g-StorageGet',key:'k'});const next=frames[1];assert.ok(next.params.context.timeout_ms>0&&next.params.context.timeout_ms<=250);
 f.core.reply(JSON.stringify({jsonrpc:'2.0',id:next.id,result:{found:false}}));await get;
 const second=f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:1000});f.core.reply(JSON.stringify({jsonrpc:'2.0',id:frames[2].id,result:{...result,remaining_budgets:{timeout_ms:10000}}}));await second;
 assert.ok(f.scope.lease.budgetEnd<=budget);assert.equal(f.scope.lease.bytes,0);
});
test('renewal relative timeout is anchored before delayed continuation and terminal never revives',async t=>{
 const f=fixture(t),frames=[];f.core.publish=frame=>{frames.push(JSON.parse(frame));return Promise.resolve();};
 const renew=f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:2000});const req=frames[0];
 f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,result:{binding_id:'binding-example',expires_at:'2099-01-01T00:00:00Z',remaining_budgets:{timeout_ms:100}}}));
 f.scope.reply({jsonrpc:'2.0',id:17,result:{ok:true}});
 await assert.rejects(renew,error=>error.code==='target_unavailable');assert.equal(f.scope.lease.end,undefined);await f.scope.terminalDone;
});
test('renewal refuses existing expiry and releases its guard on typed refusal',async t=>{
 const f=fixture(t);f.scope.lease.end=performance.now()-1;f.core.publish=()=>assert.fail('expired renewal published');
 await assert.rejects(f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:1000}),error=>error.name==='DeadlineExceededError');assert.equal(f.scope.lease.renewPending,false);
});
test('published tool cancellation preserves unknown outcome on its result promise',async t=>{
 const f=fixture(t);f.core.publishCall=(_frame,_signal,start)=>{start();return Promise.resolve();};f.core.publishControl=()=>Promise.resolve();
 const caller=new AbortController();const call=f.client.mcpCallTool(toolArgs,{signal:caller.signal});caller.abort();await assert.rejects(call.result,error=>error.code==='unknown_outcome'&&error.effect_state==='unknown');
});
test('receipt-relative remaining time is not restarted at a later helper',async t=>{
 const f=fixture(t),frames=[];f.core.publish=frame=>{frames.push(JSON.parse(frame));return Promise.resolve();};
 const renew=f.client.bindingsRenew({grant_id:'g-storage',requested_lease_ms:2000});f.core.reply(JSON.stringify({jsonrpc:'2.0',id:frames[0].id,result:{binding_id:'binding-example',expires_at:'2099-01-01T00:00:00Z',remaining_budgets:{timeout_ms:100}}}));const afterReply=performance.now();await renew;
 assert.ok(f.scope.lease.budgetEnd<=afterReply+100);f.scope.lease.budgetEnd=performance.now()-1;
 await assert.rejects(f.client.storageGet({grant_id:'g-StorageGet',key:'k'}),error=>error.name==='DeadlineExceededError');assert.equal(frames.length,1);
});
