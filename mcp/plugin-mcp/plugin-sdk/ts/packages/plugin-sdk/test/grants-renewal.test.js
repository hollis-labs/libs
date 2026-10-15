import {test} from 'node:test';
import assert from 'node:assert/strict';
import {GrantState,grantContext,currentGrantSet,decodeGrantsRenewParams,decodeGrantsRenewResult} from '../dist/grants-renewal.js';
import {decodeGrantSet} from '../dist/init-contract.js';
const owner={host_instance:'host',owner_id:'plugin',owner_generation:1};
function fixture(){
 const now=Date.now();const old=decodeGrantSet(JSON.stringify([{grant_id:'query',name:'host.nanite.readonly.query',schema_version:1,scope:{resources:['sessions'],all_sessions:true},...owner,audience:'nanite',issued_at:new Date(now-60000).toISOString(),expires_at:new Date(now+3600000).toISOString(),policy_revision:'approved'}]));
 const next=structuredClone(old);next[0].issued_at=new Date(now).toISOString();next[0].expires_at=new Date(now+86400000).toISOString();
 const state=new GrantState();state.activate(owner,old);const ctx=grantContext({signal:new AbortController().signal},state);
 const params=decodeGrantsRenewParams(JSON.stringify({renewal_version:1,sequence:1,incarnation:owner,grants:next,context:{timeout_ms:1000}}));return {state,ctx,old,next,params};
}
test('actual renewal state replaces detached metadata only after callback acceptance',async()=>{
 const f=fixture();let release;const barrier=new Promise(r=>release=r);let entered=false;
 const pending=f.state.renew(f.ctx,f.params,{async grantsRenewed(_ctx,grants){entered=true;grants[0].scope.resources.push('usage');await barrier;}});
 assert.equal(entered,true);assert.equal(currentGrantSet(f.ctx)[0].expires_at,f.old[0].expires_at);
 release();const ack=await pending;assert.deepEqual(decodeGrantsRenewResult(JSON.stringify(ack)),{renewal_version:1,sequence:1,incarnation:owner});
 assert.deepEqual(currentGrantSet(f.ctx),f.next);const detached=currentGrantSet(f.ctx);detached[0].scope.resources.push('usage');assert.deepEqual(currentGrantSet(f.ctx),f.next);
 await assert.rejects(f.state.renew(f.ctx,f.params,{grantsRenewed(){assert.fail('replay callback');}}));
});
test('exact scope bytes and authority changes refuse before callback',async()=>{
 for(const key of ['grant_id','name','schema_version','audience','policy_revision','scope','owner_generation']){
  const f=fixture();f.params.grants[0][key]=key==='schema_version'||key==='owner_generation'?2:key==='scope'?{resources:['sessions','usage'],all_sessions:true}:'other';
  await assert.rejects(f.state.renew(f.ctx,f.params,{grantsRenewed(){assert.fail(key);}}));
 }
 const f=fixture(),raw=JSON.stringify(f.params).replace('"resources":[','"resources": [');
 await assert.rejects(f.state.renew(f.ctx,decodeGrantsRenewParams(raw),{grantsRenewed(){assert.fail('scope bytes');}}));
});
test('revocation during callback and callback failure fence discovery; never revive',async()=>{
 for(const fail of [false,true]){
  const f=fixture();await assert.rejects(f.state.renew(f.ctx,f.params,{grantsRenewed(){if(fail)throw new Error('failed');f.state.end();}}));
  assert.equal(currentGrantSet(f.ctx),undefined);await assert.rejects(f.state.renew(f.ctx,f.params,{grantsRenewed(){assert.fail('revival');}}));
 }
});
test('strict update and ack parsers preserve closed fields and numeric spelling',()=>{
 const f=fixture();for(const raw of [JSON.stringify({...f.params,extra:true}),JSON.stringify(f.params).replace('"sequence":1','"sequence":1e0'),JSON.stringify(f.params).replace('"timeout_ms":1000','"timeout_ms":0')])assert.throws(()=>decodeGrantsRenewParams(raw));
 for(const raw of ['{"renewal_version":1,"sequence":1,"incarnation":null}',JSON.stringify({renewal_version:1,sequence:1,incarnation:owner,extra:true})])assert.throws(()=>decodeGrantsRenewResult(raw));
});

// The existing private negotiated-client seam exercises actual SDK helpers;
// the host reply is synthetic and confers no application authority.
import {readFileSync} from 'node:fs';
import {Writable} from 'node:stream';
import {Admission} from '../dist/admission.js';
import {Correlation} from '../dist/correlation.js';
import {FrameWriter} from '../dist/publication.js';
import {hostClientContext} from '../dist/host-client.js';
import {SecretTracker} from '../dist/log.js';
import {encodeBoundedJSON} from '../dist/frame-codec.js';
test('new actual HostClient uses accepted lease; old client retains its expiry ceiling',async t=>{
 const document=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/host-storage.json',import.meta.url),'utf8'));
 const init=structuredClone(document.init),now=Date.now();
 for(const g of init.grants){g.issued_at=new Date(now-60000).toISOString();g.expires_at=new Date(now+1000).toISOString();}
 init.host_services.limits.method_timeout_ms['host/storage/get']=2000;
 const state=new GrantState();state.activate(init.incarnation,init.grants);
 const output=new Writable({write(_raw,_enc,done){done();}}),secrets=new SecretTracker();
 const writer=new FrameWriter(output,1000,error=>assert.fail(error)),core=new Correlation(true);
 core.methodTimeoutMS={...init.host_services.limits.method_timeout_ms};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';
 const admission=new Admission(writer,core,core.encode,error=>assert.fail(error));
 const context={signal:new AbortController().signal,logger:{warn(){},debug(){},info(){},error(){}},config:{}};
 core.admit(17);const scope=admission.begin(context,{jsonrpc:'2.0',id:17,method:'plugin/health',params:{context:{binding_id:'binding-example',timeout_ms:10000}}},performance.now());
 const ctx=grantContext(scope.ctx,state),old=hostClientContext(ctx,core,init,secrets).host;
 t.after(async()=>{scope.reply({jsonrpc:'2.0',id:17,result:{ok:true}});await scope.terminalDone;scope.finish();await scope.executionDone;await writer.flush();core.close();writer.abort(new Error('closed'));output.destroy();});
 const next=structuredClone(init.grants);for(const g of next){g.issued_at=new Date(now).toISOString();g.expires_at=new Date(now+86400000).toISOString();}
 await state.renew(ctx,decodeGrantsRenewParams(JSON.stringify({renewal_version:1,sequence:1,incarnation:init.incarnation,grants:next,context:{timeout_ms:1000}})),{grantsRenewed(){}});
 const fresh=hostClientContext(ctx,core,init,secrets).host,budgets=[];
 core.publish=frame=>{const req=JSON.parse(frame);budgets.push(req.params.context.timeout_ms);core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,result:{found:false}}));return Promise.resolve();};
 const args=document.cases.find(c=>c.helper==='storageGet'&&!c.expected_error).args;
 await old.storageGet(args);await fresh.storageGet(args);
 assert.ok(budgets[0]<=1000);assert.ok(budgets[1]>1000);
 state.end();await assert.rejects(fresh.storageGet(args),error=>error.code==='target_unavailable');
});

import {PassThrough} from 'node:stream';
import {createInterface} from 'node:readline';
import {serve} from '../dist/index.js';
import {fixturePlugin,initParams} from './fixtures.js';
test('normal Serve advertises renewal, emits real ack, then reads replacement in later handler',{timeout:5000},async t=>{
 const f=fixture(),input=new PassThrough(),output=new PassThrough(),lines=createInterface({input:output}),replies=lines[Symbol.asyncIterator]();
 const plugin={...fixturePlugin('base'),grantsRenewed(_ctx,_grants){},health(ctx){assert.deepEqual(currentGrantSet(ctx),f.next);return {ok:true};}};
 const done=serve(plugin,{input,output,stderr:new Writable({write(_b,_e,cb){cb();}}),handleSignals:false});
 t.after(()=>{input.destroy();output.destroy();lines.close();});
 const send=(id,method,params)=>input.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');
 const reply=async()=>JSON.parse((await replies.next()).value);
 send(1,'plugin/init',{...initParams({incarnation:owner,grants:f.old}),grants_renewal_version:1});assert.equal((await reply()).result.grants_renewal_version,1);
 send(2,'plugin/grants/renew',f.params);assert.deepEqual((await reply()).result,{renewal_version:1,sequence:1,incarnation:owner});
 send(3,'plugin/health',{});assert.equal((await reply()).result.ok,true);
 send(4,'plugin/unload',{});assert.equal((await reply()).result.ok,true);input.end();await done;
});

test('accepted renewal refreshes cleanup discovery without sharing mutable metadata',async()=>{
 const {ReverseNegotiation}=await import('../dist/negotiation.js');
 const f=fixture();const n=new ReverseNegotiation(false,{}, {}, {}, {},()=>{});
 // The prepared private snapshot is the source used by cleanup client factories.
 n.input={grants:f.old};n.acceptedGrants(f.next);
 f.next[0].scope.resources.push('usage');
 assert.equal(n.input.grants[0].expires_at,f.params.grants[0].expires_at);
 assert.deepEqual(n.input.grants[0].scope,{resources:['sessions'],all_sessions:true});
});
