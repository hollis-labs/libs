import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import * as sdk from '../dist/index.js';
import { parseJSONTokens } from '../dist/strict-json.js';
import { Dispatcher, decodeRequest } from '../dist/dispatch.js';
import { hookResponseJSON } from '../dist/hooks-dispatch.js';
import { enableHooksFixture } from '../dist/hooks-fixture.js';
import { fixturePlugin, initParams } from './fixtures.js';
const root=new URL('../../../../protocol/v2/fixtures/hooks.json',import.meta.url);
const vectors=parseJSONTokens(await readFile(root,'utf8'));
for(const v of vectors) test('hook DTO: '+v.name,()=>{
  const validate=()=>{
    let result;
    switch(v.dto){
      case 'HookHandleParams':result=sdk.decodeHookHandleParams(v.raw);if(v.notification!==undefined)sdk.validateHookRequest(result,v.notification);break;
      case 'HookHandleBatchParams':result=sdk.decodeHookHandleBatchParams(v.raw);if(v.notification!==undefined)sdk.validateHookBatchRequest(result,v.notification);break;
      case 'HookHandleResult':result=sdk.decodeHookHandleResult(v.raw);if(v.request)sdk.validateHookResultFor(v.request,result);break;
      case 'HookHandleBatchResult':result=sdk.decodeHookHandleBatchResult(v.raw);break;
      default:throw new Error('unhandled vector '+v.dto);
    }
    return result;
  };
  if(v.valid) assert.doesNotThrow(validate);else assert.throws(validate);
});
const base=vectors[0];
const context=()=>({signal:new AbortController().signal,config:new sdk.ConfigReader({}),logger:{debug(){},info(){},warn(){},error(){}}});
async function dispatcher(plugin,enabled=true){
  if(enabled) enableHooksFixture(plugin);
  const d=new Dispatcher(plugin,context(),new sdk.SecretTracker());
  const r=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()})));
  assert.equal(r.result.hooks_profile_version,undefined);
  return d;
}
test('RawJSON preserves literals and escaped keys through author and wire paths',async()=>{
  const raw=base.raw.replace('{"title":"old"}', '{"n":1.50,"large":9007199254740993,"x\\u005b":"<x>&"}');
  const p=sdk.decodeHookHandleParams(raw);
  assert.equal(p.payloadJSON,'{"n":1.50,"large":9007199254740993,"x\\u005b":"<x>&"}');
  assert.equal(p.payload['x['],'<x>&');
  assert.ok(sdk.encodeHookHandleParams(p).includes(p.payloadJSON));
  const result={invocation_id:p.invocation_id,status:'ok',payloadJSON:p.payloadJSON};
  assert.ok(sdk.encodeHookHandleResult(result).includes(p.payloadJSON));
  const r=sdk.decodeHookHandleResult(sdk.encodeHookHandleResult(result));
  assert.equal(sdk.hookPayloadJSON(r),p.payloadJSON);
  assert.ok(sdk.encodeHookHandleResult(r).includes(p.payloadJSON));
  assert.throws(()=>sdk.encodeHookHandleResult({...result,payload:{}}));
  assert.throws(()=>sdk.rawJSON('{"a":1,"a":2}'));
  assert.throws(()=>sdk.rawJSON('1e999'));
  const d=await dispatcher(fixturePlugin('hooks-fixture'));
  const reply=await d.dispatch(decodeRequest('{"jsonrpc":"2.0","id":7,"method":"hook/handle","params":'+raw+'}'));
  assert.ok(hookResponseJSON(reply).includes(p.payloadJSON));
});
test('all batch params validate before any invocation',async()=>{
  let calls=0;
  const plugin={...fixturePlugin('hooks-fixture'),hookHandle(_ctx,p){calls++;return {invocation_id:p.invocation_id,status:'ok'};}};
  const d=await dispatcher(plugin);
  const p={...JSON.parse(base.raw),kind:'action',mode:'parallel'};
  const items=[p,{...p,invocation_id:'second',context:{timeout_ms:1}}];
  const response=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:8,method:'hook/handle_batch',params:{items}})));
  assert.equal(response.error.code,-32602);assert.equal(response.error.data.contract,'hooks/1');assert.equal(calls,0);
});
test('shipped entry points cannot bypass negotiation, and neither sentinel is a veto',async()=>{
  assert.equal(sdk.enableHooksFixture,undefined);
  await assert.rejects(import('@hollis-labs/plugin-sdk/hooks-fixture'),{code:'ERR_PACKAGE_PATH_NOT_EXPORTED'});
  const d=await dispatcher(fixturePlugin('hooks-negotiated'),false);
  const r=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:7,method:'hook/handle',params:JSON.parse(base.raw)})));
  assert.equal(r.error.code,-32601);
  assert.throws(()=>sdk.hookRPCError(-32003,'invalid_params'));
  assert.throws(()=>sdk.hookRPCError(-32010,'invalid_params'));
});
test('hook timeout and caller cancellation are operational failures',async()=>{
  const plugin=fixturePlugin('hooks-fixture');const d=await dispatcher(plugin);
  const p={...JSON.parse(base.raw),kind:'action',mode:'sequential',metadata:{fixture:'wait'},context:{binding_id:'b',timeout_ms:10},aggregate_budget_ms:5};
  const r=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:7,method:'hook/handle',params:p})));
  assert.equal(r.result.status,'failed');assert.equal(r.result.error.code,'deadline_exceeded');
  const controller=new AbortController();d.context={...d.context,signal:controller.signal};controller.abort();
  const c=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:8,method:'hook/handle',params:p})));
  assert.equal(c.result.error.code,'caller_cancelled');
});
const directory=new URL('../../../../docs/protocol/v2/transcripts/',import.meta.url);
for(const name of (await readdir(directory)).filter(n=>n.startsWith('hooks-')&&n.endsWith('.json'))) {
  test('real Node hook transcript: '+name,{timeout:15000},async t=>{
    const fixture=parseJSONTokens(await readFile(new URL(name,directory),'utf8'));
    const child=spawn(process.execPath,[new URL('./hooks-child.js',import.meta.url).pathname,fixture.profile],{stdio:['pipe','pipe','pipe']});
    const exited=new Promise((resolve,reject)=>{child.on('error',reject);child.on('exit',(code,signal)=>resolve({code,signal}));});
    let stderr='';child.stderr.on('data',b=>stderr+=b);
    t.after(()=>child.kill());
    const lines=createInterface({input:child.stdout,crlfDelay:Infinity});t.after(()=>lines.close());const replies=lines[Symbol.asyncIterator]();
    for(const step of fixture.steps) {
      child.stdin.write((step.raw??JSON.stringify(step.send))+'\n');
      if(step.expect===undefined)continue;
      const line=await replies.next();assert.equal(line.done,false,stderr);if(step.expect_contains)assert.ok(line.value.includes(step.expect_contains),line.value);const actual=parseJSONTokens(line.value);
      if(step.message_prefix){assert.ok(actual.error.message.startsWith(step.message_prefix));delete actual.error.message;}
      assert.deepEqual(actual,step.expect);
    }
    child.stdin.end();for await(const extra of {[Symbol.asyncIterator]:()=>replies})assert.fail('unexpected child reply '+extra);
    assert.deepEqual(await exited,{code:0,signal:null},stderr);
  });
}

test('raw hook responses respect narrowed frame limits',async()=>{
  const d=await dispatcher(fixturePlugin('hooks-fixture'));
  const p={...JSON.parse(base.raw),payload:'x'.repeat(1024)};
  const r=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:7,method:'hook/handle',params:p})));
  assert.throws(()=>hookResponseJSON(r,511),sdk.FrameTooLargeError);
  assert.ok(hookResponseJSON(r,4095).includes('x'.repeat(1024)));
  const raw=sdk.rawJSON(JSON.stringify('x'.repeat(800000)));
  const authored={invocation_id:'call-1',status:'ok',payloadJSON:raw};
  assert.ok(sdk.encodeHookHandleResult(authored).includes(raw));
});

test('programmable bridge fixture preserves scripts and rejects invalid output',async()=>{
  const plugin=fixturePlugin('hooks-fixture'),p=sdk.decodeHookHandleParams(base.raw);
  const literal='{"n":1.50,"large":9007199254740993,"x\\u005b":"<x>&"}';
  p.metadata={fixture:'script',script:'{"status":"ok","payload":'+literal+'}'};
  const result=await plugin.hookHandle(context(),p);
  assert.equal(sdk.hookPayloadJSON(result),literal);
  p.metadata.script='{"status":"ok","payload":{},"unknown":true}';
  assert.equal((await plugin.hookHandle(context(),p)).status,'invalid');
  for(const code of ['remote_not_allowed','latency_budget_exceeded','stale_scope','stale_binding','capacity_exhausted','deadline_exceeded','caller_cancelled','depth_exceeded','callback_cycle','transport_failure','handler_panic','invalid_output','handler_error','schema_mismatch','profile_unavailable']) {
    p.metadata={fixture:'fail:'+code};assert.equal((await plugin.hookHandle(context(),p)).error.code,code);
  }
});

test('negotiation stays on the connection when the author object is reused',async()=>{
  const plugin=fixturePlugin('hooks-negotiated');
  for(const offered of [true,false]) {
    const d=new Dispatcher(plugin,context(),new sdk.SecretTracker());
    const params=initParams(offered?{hooks_profile:{hooks_profile_version:1}}:{});
    const init=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:1,method:'plugin/init',params})));
    assert.equal(init.error,undefined);
    assert.equal(init.result.hooks_profile_version,offered?1:undefined);
    assert.equal(init.result.reverse_rpc_version,undefined);
    const reply=await d.dispatch(decodeRequest('{"jsonrpc":"2.0","id":2,"method":"hook/handle","params":'+base.raw+'}'));
    if(offered) assert.equal(reply.result.status,'ok');
    else {assert.equal(reply.error.code,-32601);assert.equal(reply.error.data.code,'profile_unavailable');}
  }
});

test('authoring an acknowledgement or mutating Init input cannot create a host offer',async()=>{
  const plugin={...fixturePlugin('hooks-negotiated'),init(_ctx,input){
    input.hooks_profile={hooks_profile_version:1};
    return {id:'fixture',name:'Fixture',version:'1.0.0',description:'conformance',protocol:2,capability_contract:1,hooks_profile_version:1};
  }};
  const d=new Dispatcher(plugin,context(),new sdk.SecretTracker());
  const init=await d.dispatch(decodeRequest(JSON.stringify({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()})));
  assert.equal(init.error,undefined);assert.equal(init.result.hooks_profile_version,undefined);
  const reply=await d.dispatch(decodeRequest('{"jsonrpc":"2.0","id":2,"method":"hook/handle","params":'+base.raw+'}'));
  assert.equal(reply.error.code,-32601);assert.equal(reply.error.data.code,'profile_unavailable');
});
