import {test} from 'node:test';
import {once} from 'node:events';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {PassThrough,Writable} from 'node:stream';
import {createInterface} from 'node:readline';
import {Correlation,CorrelationError,replyCandidate} from '../dist/correlation.js';
import {decodeRequest} from '../dist/dispatch.js';
import {EnvelopeFault} from '../dist/envelope.js';
import {encodeBoundedJSON} from '../dist/frame-codec.js';
import {FrameWriter} from '../dist/publication.js';
import {serveConnection} from '../dist/serve.js';
import {fixturePlugin,initParams} from './fixtures.js';
for(const name of ['duplex-correlation','duplex-invalid']){
 const corpus=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/'+name+'.json',import.meta.url),'utf8'));
 for(const vector of corpus.cases)test('duplex core: '+vector.name,async()=>{
  const c=fixtureCorrelation(vector.directional);const frames=[],calls=new Map();let next=0;
  c.encode=value=>encodeBoundedJSON(value,1024*1024)+'\n';
  c.publish=frame=>{frames.push(JSON.parse(frame));if(vector.immediate)c.reply(JSON.stringify({jsonrpc:'2.0',id:frames.at(-1).id,result:{accepted:true}}));return Promise.resolve();};
  for(const op of vector.operations){
   let failure;
   try{
    switch(op.op){
     case 'admit':c.admit(op.id);break;
     case 'release':c.release(op.id);break;
     case 'assert_inbound':assert.ok(c.incoming.has(op.id));break;
     case 'call':calls.set(++next,c.call(op.method,op.params_raw).then(result=>({result}),error=>({error})));break;
     case 'expect_frame':{const frame=frames.shift();assert.equal(frame.id,op.id);assert.equal(frame.method,'host/log');break;}
     case 'receive':{
      try{const request=decodeRequest(op.raw);if(!request){if(c.directional)c.reply(op.raw);}else c.admit(request.id);}
      catch(error){if(error instanceof EnvelopeFault){if(op.recover){assert.equal(error.code,op.recover);break;}if(c.directional&&(error.code===-32700||replyCandidate(op.raw)))throw new CorrelationError();}throw error;}
      break;
     }
     case 'expect_result':assert.deepEqual(await calls.get(op.id),{result:op.result});break;
     case 'expect_error':{const result=await calls.get(op.id);assert.ok(result.error);if(op.code)assert.equal(result.error.code,op.code);break;}
     case 'close':c.close();break;
     default:assert.fail(op.op);
    }
   }catch(error){failure=error;}
   assert.equal(Boolean(failure),Boolean(op.fault),`${op.op}: ${failure}`);if(failure)c.close(failure);
  }
  c.close();
 });
}
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {resolve,promise};};
const logParams='{"grant_id":"g","context":{"binding_id":"b","timeout_ms":10000,"parent_call":{"request_owner":"host","id":1}},"level":"info","message":"ready"}';
async function rig(t,plugin,core){
 const input=new PassThrough(),output=new PassThrough();const lines=createInterface({input:output});const replies=lines[Symbol.asyncIterator]();
 const done=serveConnection(plugin,{input,output,stderr:new Writable({write(_b,_e,cb){cb();}})},core).then(()=>undefined,error=>error);
 t.after(()=>{input.destroy();output.destroy();lines.close();});
 return {input,done,send(value){input.write(JSON.stringify(value)+'\n');},async reply(){return JSON.parse((await replies.next()).value);}};
}
const runtimeCorpus=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/duplex-correlation.json',import.meta.url),'utf8'));
function subset(actual,expected){for(const [key,value] of Object.entries(expected)){if(value&&typeof value==='object')subset(actual[key],value);else assert.deepEqual(actual[key],value);}}
for(const vector of runtimeCorpus.runtime)test('shared duplex runtime: '+vector.name,{timeout:5000},async t=>{
 const core=fixtureCorrelation(vector.directional);let initialized=false,health_calls=0,unload_attempts=0;
 const plugin={...fixturePlugin('base'),async init(){assert.deepEqual(await core.call('host/log',logParams),{accepted:true});initialized=true;return fixturePlugin('base').init();},async unload(){unload_attempts++;assert.deepEqual(await core.call('host/log',logParams),{accepted:true});},health(){health_calls++;return {ok:true};}};
 const r=await rig(t,plugin,core);
 for(const step of vector.steps){switch(step.op){
 case 'send_host':case 'send_host_response':r.send(step.send);break;
 case 'expect_plugin_request':case 'expect_plugin_response':{const response=await r.reply();subset(response,step.expect);if(response.result)assert.equal(response.result.reverse_rpc_version,undefined);break;}
 case 'expect_exit':assert.equal(await r.done,undefined);subset({initialized,health_calls,unload_attempts},step.effects);break;
 case 'barrier':subset({initialized,health_calls,unload_attempts},step.effects);break;
 default:assert.fail(step.op);
 }}
});
test('base duplicate live request fences before a second callback/reply',{timeout:5000},async t=>{
 const started=deferred(),release=deferred();let calls=0;
 t.after(()=>release.resolve());const plugin={...fixturePlugin('base'),async command(ctx){calls++;started.resolve();await once(ctx.signal,'abort');return {action:'noop'};}};
 const r=await rig(t,plugin,fixtureCorrelation());r.send({jsonrpc:'2.0',id:8000,method:'plugin/init',params:initParams()});await r.reply();
 const request={jsonrpc:'2.0',id:'live',method:'command/execute',params:{name:'hold',args:'',session_id:''}};r.send(request);await started.promise;r.send(request);release.resolve();assert.ok(await r.done instanceof CorrelationError);assert.equal(calls,1);
});
test('receipt keeps incoming ID live until whole write and fails pending on write error',{timeout:5000},async()=>{
 let callback;const output=new Writable({write(_b,_e,cb){callback=cb;}});const c=fixtureCorrelation();c.admit('held');
 const writer=new FrameWriter(output,100,error=>c.close(error));const receipt=writer.publish('{}\n').then(()=>c.release('held'),error=>{c.close(error);throw error;});
 assert.throws(()=>c.admit('held'),CorrelationError);callback();await receipt;c.admit('held');writer.abort(new Error('closed'));output.destroy();
 const reverse=fixtureCorrelation(true);reverse.encode=value=>encodeBoundedJSON(value,1024)+'\n';reverse.publish=()=>Promise.reject(new Error('write failed'));await assert.rejects(reverse.call('host/log',logParams),error=>error.code==='target_unavailable'&&error.cause.message==='write failed');
 reverse.encode=()=>{throw new Error('encode failed');};await assert.rejects(reverse.call('host/log',logParams),/encode failed/);
});

test('EOF fails a pending reverse call and admits no new reverse publication',{timeout:5000},async t=>{
 const core=fixtureCorrelation(true);const failed=deferred();
 const plugin={...fixturePlugin('base'),async init(){try{await core.call('host/log',logParams);}catch(error){failed.resolve(error);throw error;}return fixturePlugin('base').init();}};
 const r=await rig(t,plugin,core);r.send({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()});assert.equal((await r.reply()).method,'host/log');
 r.input.end();assert.ok(await failed.promise instanceof Error);assert.equal((await r.reply()).error.code,-32603);assert.equal(await r.done,undefined);await assert.rejects(core.call('host/log',logParams));
});
test('abort rejects active and queued receipts once, ignoring a late callback',{timeout:5000},async()=>{
 let callback;const output=new Writable({write(_b,_e,cb){callback=cb;}});const writer=new FrameWriter(output,100,()=>{});
 const first=writer.publish('{}\n');const second=writer.publish('{}\n');const results=Promise.allSettled([first,second]);writer.abort(new Error('closed'));
 assert.deepEqual((await results).map(r=>r.status),['rejected','rejected']);callback();await assert.rejects(writer.flush(),/closed/);output.destroy();
});

test('unload retains admission readiness while a cancelled Init later succeeds',{timeout:5000},async t=>{
 const started=deferred();let unloads=0;
 const plugin={...fixturePlugin('base'),async init(ctx){started.resolve();await once(ctx.signal,'abort');return fixturePlugin('base').init();},unload(){unloads++;}};
 const r=await rig(t,plugin,fixtureCorrelation());r.send({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()});await started.promise;r.send({jsonrpc:'2.0',id:2,method:'plugin/unload'});
 assert.equal((await r.reply()).id,1);const terminal=await r.reply();assert.equal(terminal.id,2);assert.equal(terminal.error.message,'successful init required');assert.equal(await r.done,undefined);assert.equal(unloads,1);
});

// Fixture-only ceilings; real peers supply HostServiceLimits.method_timeout_ms.
function fixtureCorrelation(directional=false){const core=new Correlation(directional);core.methodTimeoutMS={"host/log":10000};return core;}
