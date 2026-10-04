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
const documents=['storage','secrets','egress','events-log'].map(name=>JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/host-'+name+'.json',import.meta.url),'utf8')));
function fixture(t,init){
 const output=new Writable({write(_raw,_enc,done){done();}}),secrets=new SecretTracker();
 const writer=new FrameWriter(output,1000,error=>assert.fail(error));
 const core=new Correlation(true);core.methodTimeoutMS={...init.host_services.limits.method_timeout_ms};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';
 const admission=new Admission(writer,core,core.encode,error=>assert.fail(error));
 const context={signal:new AbortController().signal,logger:{warn(){},debug(){},info(){},error(){}},config:{}};
 core.admit(17);const scope=admission.begin(context,{jsonrpc:'2.0',id:17,method:'plugin/health',params:{context:{binding_id:'binding-example',timeout_ms:10000}}},performance.now());assert.ok(scope);
 const ctx=hostClientContext(scope.ctx,core,init,secrets);
 t.after(async()=>{scope.reply({jsonrpc:'2.0',id:17,result:{ok:true}});await scope.terminalDone;scope.finish();await scope.executionDone;await writer.flush();core.close();writer.abort(new Error('closed'));output.destroy();});
 return {ctx,client:ctx.host,core,scope,secrets};
}
for(const doc of documents)for(const v of doc.cases)test('shared author helper: '+v.name,async t=>{
 const f=fixture(t,doc.init);let calls=0;
 f.core.publish=frame=>{
  calls++;const req=JSON.parse(frame);assert.equal(req.method,v.method);
  const {context,...args}=req.params;assert.deepEqual(args,v.args);assert.equal(context.binding_id,'binding-example');assert.deepEqual(context.parent_call,{request_owner:'host',id:17});assert.ok(context.timeout_ms>0&&context.timeout_ms<=1000);
  try{f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,...(v.error?{error:v.error}:{result:v.result})}));}catch(error){f.core.close(error);}
  return Promise.resolve();
 };
 if(v.expected_error?.startsWith('host:')){await assert.rejects(f.client[v.helper](v.args),error=>error instanceof HostRPCFailure&&JSON.stringify(error.data)===JSON.stringify(v.error.data));return;}
 if(v.expected_error==='invalid_input'){await assert.rejects(f.client[v.helper](v.args));assert.equal(calls,0);return;}
 if(v.expected_error){await assert.rejects(f.client[v.helper](v.args),error=>error.code===v.expected_error);if(v.expected_error==='capability_denied')assert.equal(calls,0);return;}
 const result=await f.client[v.helper](v.args);assert.equal(calls,1);
 if(v.helper==='secretsGet'){
  assert.deepEqual(result.value,Uint8Array.from(Buffer.from(v.result.value_base64,'base64')));assert.equal(result.expires_at,v.result.expires_at);
  if(result.value.length){assert.equal(f.secrets.redact(v.result.value_base64),'[REDACTED]');try{const text=new TextDecoder('utf8',{fatal:true}).decode(result.value);assert.equal(f.secrets.redact(text),'[REDACTED]');}catch(error){if(error instanceof assert.AssertionError)throw error;}}
 }else assert.deepEqual(result,v.result);
});
test('grant snapshot and live scope cannot be replaced by author input',async t=>{
 const init=structuredClone(documents[0].init),f=fixture(t,init),args=documents[0].cases[0].args;init.grants=[];init.host_services.limits.method_timeout_ms['host/storage/get']=0;
 f.core.publish=frame=>{const req=JSON.parse(frame);f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,result:{found:false}}));return Promise.resolve();};
 assert.deepEqual(await f.client.storageGet(args),{found:false});
 f.scope.reply({jsonrpc:'2.0',id:17,result:{ok:true}});await f.scope.terminalDone;
 await assert.rejects(f.client.storageGet(args),error=>error.code==='target_unavailable');
});
test('host refusal remains typed and grant expiry prevents publication',async t=>{
 const doc=documents[0],f=fixture(t,doc.init);f.core.publish=frame=>{const req=JSON.parse(frame);f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,error:{code:-32010,message:'safe refusal',data:{contract:'host-rpc/1',code:'capability_denied',effect_state:'not_started',retryable:false,request_id:req.id}}}));return Promise.resolve();};
 await assert.rejects(f.client.storageGet(doc.cases[0].args),error=>error instanceof HostRPCFailure&&error.data.code==='capability_denied');
 const init=structuredClone(doc.init);init.grants[0].expires_at='2001-01-01T00:00:00Z';const expired=fixture(t,init);expired.core.publish=()=>assert.fail('expired grant published');
 await assert.rejects(expired.client.storageGet(doc.cases[0].args),error=>error.name==='DeadlineExceededError');
});
test('secrets are redacted in host log text, names and nested values before transmission',async t=>{
 const f=fixture(t,documents[0].init);f.secrets.add('sensitive');f.secrets.add('c2Vuc2l0aXZl');
 f.core.publish=frame=>{const req=JSON.parse(frame);assert.equal(req.params.message,'use [REDACTED]');assert.deepEqual(req.params.fields,[{name:'[REDACTED]',value:{nested:['[REDACTED]','[REDACTED]']}}]);f.core.reply(JSON.stringify({jsonrpc:'2.0',id:req.id,result:{accepted:true}}));return Promise.resolve();};
 await f.client.log({grant_id:'g-Log',level:'info',message:'use sensitive',fields:[{name:'sensitive',value:{nested:['sensitive','c2Vuc2l0aXZl']}}]});
});
test('caller budget narrows wire timeout and pending child retains parent permit',async t=>{
 const f=fixture(t,documents[0].init);let transmitted;const publication=new Promise(resolve=>{transmitted=resolve;});
 f.core.publishCall=(frame,_signal,start)=>{start();const req=JSON.parse(frame);assert.ok(req.params.context.timeout_ms>0&&req.params.context.timeout_ms<=250);transmitted();return Promise.resolve();};f.core.publishControl=()=>Promise.resolve();
 const controller=new AbortController();const call=f.client.storagePut(documents[0].cases.find(v=>v.name==='StoragePut').args,{timeoutMs:250,signal:controller.signal});
 await publication;f.scope.finish();let released=false;f.scope.executionDone.then(()=>{released=true;});await Promise.resolve();assert.equal(released,false);
 controller.abort();await assert.rejects(call,error=>error.code==='unknown_outcome'&&error.effect_state==='unknown');await f.scope.executionDone;
});
test('already cancelled caller and private verified binding expiry stay local',async t=>{
 const init=documents[0].init,f=fixture(t,init);f.core.publish=()=>assert.fail('local failure published');const caller=new AbortController();caller.abort();
 await assert.rejects(f.client.storageGet(documents[0].cases[0].args,{signal:caller.signal}),error=>error.name==='TransportCancelledError');
 const ctx=hostClientContext(f.scope.ctx,f.core,init,f.secrets,Date.now()-1000);
 await assert.rejects(ctx.host.storageGet(documents[0].cases[0].args),error=>error.name==='DeadlineExceededError');
});

test('production base and negotiated hooks contexts omit the host client',async t=>{
 const {serve}=await import('../dist/serve.js'),{fixturePlugin,initParams}=await import('./fixtures.js'),{PassThrough}=await import('node:stream'),{createInterface}=await import('node:readline');
 const corpus=JSON.parse(readFileSync(new URL('../../../../docs/protocol/v2/transcripts/hooks-negotiated.json',import.meta.url),'utf8'));
 for(const hooks of [false,true]){
  const input=new PassThrough(),output=new PassThrough(),lines=createInterface({input:output})[Symbol.asyncIterator]();let calls=0;
  const plugin={...fixturePlugin('base'),health(ctx){assert.equal(ctx.host,undefined);calls++;return {ok:true};},...(hooks?{hookHandle(ctx,p){assert.equal(ctx.host,undefined);calls++;return{invocation_id:p.invocation_id,status:'ok'};}}:{})};
  const done=serve(plugin,{input,output,stderr:new Writable({write(_raw,_e,cb){cb();}})});t.after(()=>{input.destroy();output.destroy();});
  const send=(id,method,params)=>input.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');const read=async()=>JSON.parse((await lines.next()).value);
  const init=initParams();if(hooks)init.hooks_profile={hooks_profile_version:1};send(1,'plugin/init',init);assert.equal((await read()).error,undefined);
  send(2,'plugin/health',{context:{binding_id:'b',timeout_ms:10000}});assert.equal((await read()).error,undefined);
  if(hooks){const call=corpus.steps.find(s=>s.send?.method==='hook/handle').send;send(3,call.method,call.params);assert.equal((await read()).error,undefined);}
  assert.equal(calls,hooks?2:1);input.end();await done;
 }
});
test('missing method ceiling and wrong descriptor fail before publication',async t=>{
 for(const which of ['offer','descriptor']){
  const init=structuredClone(documents[0].init);
  if(which==='offer'){delete init.host_services.limits.method_timeout_ms['host/storage/get'];init.host_services.methods=init.host_services.methods.filter(m=>m!=='host/storage/get');}
  else init.grants[0].name='storage.write';
  const f=fixture(t,init);f.core.publish=()=>assert.fail('unavailable method published');
  await assert.rejects(f.client.storageGet(documents[0].cases[0].args),error=>error.code===(which==='offer'?'unsupported_capability':'capability_denied')&&error.effect_state==='not_started');
 }
});
test('receipt binds the published operation key even if author arguments change',async t=>{
 const f=fixture(t,documents[0].init);let sent;f.core.publish=frame=>{sent=JSON.parse(frame);return Promise.resolve();};
 const args=structuredClone(documents[0].cases.find(v=>v.name==='StoragePut').args);const call=f.client.storagePut(args);assert.ok(sent);args.operation_key='changed-after-send';
 f.core.reply(JSON.stringify({jsonrpc:'2.0',id:sent.id,result:{operation_key:sent.params.operation_key,revision:'r1'}}));assert.equal((await call).operation_key,'put-1');
});
