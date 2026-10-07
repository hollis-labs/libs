import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {Writable,PassThrough} from 'node:stream';
import {createInterface} from 'node:readline';
import {Admission} from '../dist/admission.js';
import {Correlation} from '../dist/correlation.js';
import {FrameWriter} from '../dist/publication.js';
import {encodeBoundedJSON} from '../dist/frame-codec.js';
import {serve} from '../dist/serve.js';
import {fixturePlugin,initParams} from './fixtures.js';
const corpus=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/duplex-control.json',import.meta.url),'utf8'));
const host=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/host-rpc.json',import.meta.url),'utf8'));
const params=dto=>host.structural.find(v=>v.schema===dto&&v.valid).raw;
const deferred=()=>{let resolve;const promise=new Promise(r=>{resolve=r;});return{promise,resolve};};
function fixture(t,limit=8388608){
 const frames=[];const output=new Writable({write(raw,_e,done){frames.push(JSON.parse(raw));done();}});
 const writer=new FrameWriter(output,1000,error=>assert.fail(error));
 const core=new Correlation();
 const admission=new Admission(writer,core,response=>encodeBoundedJSON(response,limit-1)+'\n',error=>assert.fail(error));
 const context={signal:new AbortController().signal,logger:{warn(){},debug(){},info(){},error(){}},config:{}};
 const begin=(id,method='plugin/health',params,received=performance.now())=>{const request={jsonrpc:'2.0',id,method,params};core.admit(id);const scope=admission.begin(context,request,received);assert.ok(scope);return scope;};
 t.after(()=>{writer.abort(new Error('closed'));output.destroy();});return{frames,writer,core,admission,context,begin};
}
test('admission retains cancelled callbacks and suppresses old reused-ID output',async t=>{
 const f=fixture(t);const scopes=Array.from({length:corpus.policy.forward_slots},(_,i)=>f.begin(i+1));scopes.forEach(s=>assert.ok(s.start()));
 assert.equal(f.admission.begin(f.context,{id:'overload',method:'plugin/health'},performance.now()),undefined);
 const first=scopes[0];f.admission.cancel({request_owner:'plugin',id:1,reason:'caller_cancelled'});assert.equal(first.controller.signal.aborted,false);
 f.admission.cancel({request_owner:'host',id:999,reason:'caller_cancelled'});
 f.admission.cancel({request_owner:'host',id:1,reason:'caller_cancelled'});await first.terminalDone;
 assert.equal(f.frames[0].error.data.code,'cancelled');assert.equal(f.frames[0].error.data.effect_state,'unknown');
 assert.equal(f.admission.begin(f.context,{id:1,method:'plugin/health'},performance.now()),undefined);
 scopes[1].reply({jsonrpc:'2.0',id:2,result:{ok:true}});await scopes[1].terminalDone;scopes[1].finish();await scopes[1].executionDone;const reused=f.begin(1);reused.start();
 first.reply({jsonrpc:'2.0',id:1,result:{ok:false}});assert.equal(f.frames.length,2);first.finish();await first.executionDone;
 reused.reply({jsonrpc:'2.0',id:1,result:{ok:true}});await reused.terminalDone;reused.finish();
 for(const scope of scopes.slice(2)){scope.reply({jsonrpc:'2.0',id:scope.request.id,result:{ok:true}});await scope.terminalDone;scope.finish();}
});
test('two lifecycle slots remain available under ordinary saturation',async t=>{
 const f=fixture(t);const scopes=Array.from({length:corpus.policy.forward_slots},(_,i)=>f.begin(i+1));
 for(let i=0;i<corpus.policy.control_slots;i++)scopes.push(f.begin(100+i,'plugin/load'));
 assert.equal(f.admission.begin(f.context,{id:200,method:'plugin/load'},performance.now()),undefined);
 const total=corpus.policy.forward_slots+corpus.policy.reverse_slots+corpus.policy.control_slots;assert.ok(total<=32);assert.ok(total*corpus.policy.terminal_credit_bytes<=8388608);
 for(const scope of scopes){scope.reply({jsonrpc:'2.0',id:scope.request.id,result:{ok:true}});await scope.terminalDone;scope.finish();}
});
test('deadline starts at arrival; absent context has no timer; retained inner work holds permit',async t=>{
 const f=fixture(t);const expired=f.begin(1,'plugin/health',{context:{timeout_ms:1}},performance.now()-1000); // Fixture-only expired budget.
 assert.equal(expired.start(),false);await expired.terminalDone;expired.finish();
 assert.equal(f.frames[0].error.data.code,'deadline_exceeded');assert.equal(f.frames[0].error.data.effect_state,'not_started');
 const plain=f.begin(2);assert.equal(plain.deadline,undefined);const release=plain.retain();plain.finish();let complete=false;plain.executionDone.then(()=>{complete=true;});await Promise.resolve();assert.equal(complete,false);release();await plain.executionDone;
 plain.reply({jsonrpc:'2.0',id:2,result:{ok:true}});await plain.terminalDone;
});
test('oversized known success gets bounded committed classification',async t=>{
 const f=fixture(t,512);const scope=f.begin('write');scope.start();scope.reply({jsonrpc:'2.0',id:'write',result:{body:'x'.repeat(2048)}});await scope.terminalDone;scope.finish();
 assert.equal(f.frames[0].error.code,-32603);assert.equal(f.frames[0].error.data.code,'budget_exceeded');assert.equal(f.frames[0].error.data.effect_state,'committed');assert.ok(Buffer.byteLength(JSON.stringify(f.frames[0]))+1<=1024);
});
test('reverse queued cancellation removes bytes before publication',async t=>{
 const held=deferred();let callback;const output=new Writable({write(_raw,_e,done){callback=done;held.resolve();}});t.after(()=>output.destroy());
 const writer=new FrameWriter(output,1000,error=>assert.fail(error));t.after(()=>writer.abort(new Error('closed')));
 const blocker=writer.publish('{}\n');await held.promise;
 const core=new Correlation(true);core.methodTimeoutMS={'host/storage/put':10000};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';let controls=0;
 core.publishCall=(frame,signal,onStart)=>writer.publish(frame,'ordinary',undefined,{signal,onStart});core.publishControl=()=>{controls++;return Promise.resolve();};
 const controller=new AbortController();const call=core.call('host/storage/put',params('StoragePutParams'),{signal:controller.signal});controller.abort();
 await assert.rejects(call,error=>error.code==='cancelled'&&error.effect_state==='not_started');assert.equal(controls,0);
 callback();await blocker;await writer.flush();
});
test('published reverse mutation expires as unknown; offer clips and slots reject before execution',async()=>{
 const core=new Correlation(true);core.methodTimeoutMS={'host/storage/put':500,'host/log':500};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';const frames=[],controls=[];
 core.publishCall=(frame,_signal,onStart)=>{onStart();frames.push(JSON.parse(frame));return Promise.resolve();};core.publishControl=frame=>{controls.push(JSON.parse(frame));return Promise.resolve();};
 const controller=new AbortController();const write=core.call('host/storage/put',params('StoragePutParams'),{signal:controller.signal});assert.ok(frames[0].params.context.timeout_ms<=500);controller.abort();
 await assert.rejects(write,error=>error.code==='unknown_outcome'&&error.effect_state==='unknown');assert.equal(controls[0].params.request_owner,'plugin');
 const pending=Array.from({length:corpus.policy.reverse_slots},()=>core.call('host/log',params('LogParams')).catch(error=>error));
 await assert.rejects(core.call('host/log',params('LogParams')),error=>error.code==='rate_limited');core.close(new Error('closed'));await Promise.all(pending);
});
test('real transport routes cancellation while handler remains uncooperative',async t=>{
 const input=new PassThrough(),output=new PassThrough();const lines=createInterface({input:output})[Symbol.asyncIterator]();const started=deferred(),release=deferred();let signal;
 const plugin={...fixturePlugin('base'),async command(ctx){signal=ctx.signal;started.resolve();await release.promise;return{action:'noop'};}};
 const done=serve(plugin,{input,output,stderr:new Writable({write(_raw,_e,cb){cb();}})});t.after(()=>{release.resolve();input.destroy();output.destroy();});
 const send=(id,method,params)=>input.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');const read=async()=>JSON.parse((await lines.next()).value);
 send(1,'plugin/init',initParams());await read();send('held','command/execute',{name:'hold',args:'',session_id:''});await started.promise;
 send(undefined,'rpc/cancel',{request_owner:'host',id:'held',reason:'caller_cancelled'});const cancelled=await read();assert.equal(cancelled.error.data.contract,'plugin-rpc/2');assert.equal(cancelled.error.data.code,'unknown_outcome');assert.equal(signal.aborted,true);
 send(2,'plugin/health');assert.equal((await read()).result.ok,true);release.resolve();input.end();await done;
});

test('configured admission ceilings narrow default slots',async t=>{
 const {admissionLimits}=await import('../dist/admission.js');
 for(const limits of [{forwardSlots:-1},{forwardSlots:17},{reverseSlots:9},{controlSlots:3},{forwardSlots:1.5}])assert.throws(()=>admissionLimits(limits));
 const f=fixture(t);const narrowed=new Admission(f.writer,f.core,response=>encodeBoundedJSON(response,1048576)+'\n',error=>assert.fail(error),{forwardSlots:1,reverseSlots:1,controlSlots:1});
 const first=narrowed.begin(f.context,{id:1,method:'plugin/health'},performance.now());assert.ok(first);
 assert.equal(narrowed.begin(f.context,{id:2,method:'plugin/health'},performance.now()),undefined);first.reply({jsonrpc:'2.0',id:1,result:{ok:true}});await first.terminalDone;first.finish();
});
test('two full byte lanes plus one in-progress frame remain bounded',async t=>{
 const started=deferred();let callback;const output=new Writable({write(_raw,_e,done){callback=done;started.resolve();}});t.after(()=>output.destroy());
 const writer=new FrameWriter(output,1000,()=>{});const frame='"'+'x'.repeat(8388608-3)+'"\n';assert.equal(Buffer.byteLength(frame),8388608);
 const receipts=[writer.publish(frame).catch(error=>error)];await started.promise;receipts.push(writer.publish(frame).catch(error=>error),writer.publish(frame,'control').catch(error=>error));
 await assert.rejects(writer.publish('\n'));await assert.rejects(writer.publish('\n','control'));writer.abort(new Error('closed'));await Promise.all(receipts);callback();
});

const deadlines=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/duplex-deadlines.json',import.meta.url),'utf8'));
for(const vector of deadlines.no_default_methods)test('shared no-default-deadline: '+vector.method,async t=>{
 const f=fixture(t);const params=vector.params_raw?JSON.parse(vector.params_raw):vector.method==='plugin/init'?initParams():undefined;const scope=f.begin(1,vector.method,params);assert.equal(scope.deadline,undefined);scope.reply({jsonrpc:'2.0',id:1,result:{}});await scope.terminalDone;scope.finish();
});
const effects=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/duplex-effects.json',import.meta.url),'utf8'));
for(const vector of effects.cases)test('shared reverse effects: '+vector.name,async()=>{
 const core=new Correlation(true);core.methodTimeoutMS={[vector.method]:10000};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';core.publishCall=(_frame,_signal,start)=>{if(vector.possible)start();return Promise.resolve();};core.publishControl=()=>Promise.resolve();
 const controller=new AbortController();const result=core.call(vector.method,vector.params_raw,{signal:controller.signal});controller.abort();await assert.rejects(result,error=>error.code===vector.code&&error.effect_state===vector.effect_state);core.close(new Error('closed'));
});

test('writer checks deadline before a delayed timer can select expired bytes',async t=>{
 const {DeadlineExceededError}=await import('../dist/request-control.js');const started=deferred();let callback,writes=0,expired=false;const output=new Writable({write(_raw,_e,done){writes++;callback=done;started.resolve();}});t.after(()=>output.destroy());
 const writer=new FrameWriter(output,1000,error=>assert.fail(error));const blocker=writer.publish('{}\n');await started.promise;
 const request=writer.publish('{}\n','ordinary',undefined,{signal:new AbortController().signal,onStart:()=>assert.fail('expired publication selected'),beforeStart:()=>expired?new DeadlineExceededError():undefined});
 const rejected=assert.rejects(request,DeadlineExceededError);expired=true;callback();await blocker;await rejected;assert.equal(writes,1);await writer.flush();
});

test('reverse publication deducts writer queue time from the wire budget',async t=>{
 const started=deferred();let callback;const frames=[];
 const output=new Writable({write(raw,_encoding,done){frames.push(JSON.parse(raw));if(frames.length===1){callback=done;started.resolve();}else done();}});
 t.after(()=>output.destroy());const writer=new FrameWriter(output,1000,error=>assert.fail(error));t.after(()=>writer.abort(new Error('closed')));
 const blocker=writer.publish('{}\n');await started.promise;
 const core=new Correlation(true);core.methodTimeoutMS={'host/log':10000};core.encode=value=>encodeBoundedJSON(value,1048576)+'\n';let initial;
 core.publishCall=(frame,signal,onStart,beforeStart,prepare)=>{initial=JSON.parse(frame).params.context.timeout_ms;return writer.publish(frame,'ordinary',undefined,{signal,onStart,beforeStart,prepare});};
 const result=core.call('host/log',params('LogParams')).catch(error=>error);
 // Fixture-only delay behind a held write, not a production timeout default.
 await new Promise(resolve=>setTimeout(resolve,20));callback();await blocker;await writer.flush();
 const remaining=frames[1].params.context.timeout_ms;assert.ok(remaining>0&&remaining<initial,`${initial} -> ${remaining}`);
 core.close(new Error('closed'));await result;
});
