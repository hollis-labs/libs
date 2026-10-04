// Normal public Serve only. Host clients/grants/correlation are delivered by Init.
import {requestScope} from '../dist/admission.js';
import { serve } from '../dist/serve.js';
import { fixtureControl, fixtureEvent as event } from './child-control.js';
import { childCases } from './child-cases-worker.js';
const mode=process.argv[2].slice('negotiated:'.length), releases=new Map();
const fixture=childCases(mode,null,event,releases,true);
let cached;
const failure=e=>!e?{code:'ok',effect_state:'committed'}:(e.data??{code:e.code??'fixture_failure',effect_state:e.effect_state??'unknown'});
const observe=(ctx,kind)=>{event({kind,client:!!ctx.host});if(ctx.host)cached=ctx.host;return ctx.host;};
const log=async ctx=>{
 const h=observe(ctx,'lifecycle_client');if(!h)return;
 let err;try{await h.storageGet({grant_id:'g-StorageGet',key:'denied'});}catch(e){err=e;}
 event({kind:'lifecycle_business',failure:failure(err)});
 await h.log({grant_id:'g-Log',level:'info',message:'lifecycle'});
};
const p=fixture.plugin, baseInit=p.init, baseLoad=p.load, baseUnload=p.unload, baseHealth=p.health;
p.init=async(ctx,input)=>{
 const r=baseInit(ctx,input),h=observe(ctx,'init_client');
 if(['policy','local-limits'].includes(mode)){const a=requestScope(ctx).manager,w=a.writer;event({kind:'policy',forward:a.limits.forwardSlots,reverse:a.limits.reverseSlots,control:a.limits.controlSlots,frame:w.frameLimit,queue:w.limits.bytes,write_ms:w.timeout});}
 if(mode==='mutate'){input.host_services.methods=[];input.host_services.limits.method_timeout_ms={};input.grants=[];}
 if(['authored-only','authored-ack','observe-authored'].includes(mode))r.reverse_rpc_version=1;
 if(mode==='fallback')r.description='x'.repeat(4096);
 if(mode==='init-error')throw new Error('fixture failed Init');
 if(mode==='init-panic')throw 'fixture Init panic';
 if(mode==='lifecycle')await log(ctx);
 if(mode==='pending-init'){
  if(!h)throw new Error('no provisional client');
  void h.log({grant_id:'g-Log',level:'info',message:'pending'}).then(()=>event({kind:'pending_done',failure:failure()}),e=>event({kind:'pending_done',failure:failure(e)}));
  await new Promise(r=>releases.set('fail-init',r));throw new Error('fixture failed Init');
 }
 if(mode==='wait-init'&&!ctx.signal.aborted)await new Promise(r=>ctx.signal.addEventListener('abort',r,{once:true}));
 return r;
};
p.health=async ctx=>{
 if(mode.startsWith("expanded"))return baseHealth(ctx);
 const h=observe(ctx,'health_client');
 if(['get','mutate','authored-ack','subset'].includes(mode)){
  if(!h)return {ok:false};let err;try{await h.storageGet({grant_id:'g-StorageGet',key:'read'});}catch(e){err=e;}
  event({kind:'health_helper',failure:failure(err)});return {ok:!err};
 }
 return {ok:true};
};
p.hookHandle=(ctx,v)=>{observe(ctx,'hook_client');return {invocation_id:v.invocation_id,status:'ok',...(v.kind==='filter'?{payloadJSON:v.payloadJSON}:{})};};
p.load=ctx=>mode==='lifecycle'?log(ctx).then(()=>({})):baseLoad(ctx);
p.unload=async ctx=>{
 if(!mode.startsWith('expanded')){await baseUnload(ctx);if(mode==='lifecycle')await log(ctx);else observe(ctx,'cleanup_client');}
 else await baseUnload(ctx);
};
releases.set('snapshot',()=>event({kind:'snapshot',effects:p.effects()}));
releases.set('probe-cached',async()=>{let err;try{if(!cached)throw new Error('no cached client');await cached.log({grant_id:'g-Log',level:'info',message:'cached'});}catch(e){err=e;}event({kind:'cached_probe',failure:failure(err)});});
const stop=fixtureControl(v=>{releases.get(v.gate)?.();fixture.release(v.gate);event({kind:'control_received',seq:v.seq});});
event({kind:'ready'});
const options={...fixture.options,reverseRPC:!['no-opt-in','authored-only'].includes(mode)};
if(mode==='local-limits')Object.assign(options,{inputFrameBytes:4096,outputFrameBytes:4096,queueLimits:{frames:32,bytes:8192},writeTimeoutMs:300});
try{await serve(p,options);event({kind:'finished',effects:p.effects(),transport_error:null});}
catch(e){event({kind:'finished',effects:p.effects(),transport_error:e.name});process.exitCode=1;}
finally{stop();}
