import { cloneGrantSet, decodeInitParams, encodeInitParams, InitError } from './init-contract.js';
import { hostClientContext } from './host-client.js';
import { requestScope } from './admission.js';
import type { Admission } from './admission.js';
import type { Correlation } from './correlation.js';
import type { FrameWriter } from './publication.js';
import type { SecretTracker } from './log.js';
import type { Context } from './types.js';
import type { InitParams } from './wire.js';

/** Private connection policy; host-global limits and trusted depth stay host-owned. */
export class ReverseNegotiation {
 private input?:InitParams;
 private active=false;
 readonly optIn:boolean;readonly core:Correlation;readonly writer:FrameWriter;readonly admission:Admission;readonly secrets:SecretTracker;
 private readonly narrow:(frame:number)=>void;
 constructor(optIn:boolean,core:Correlation,writer:FrameWriter,admission:Admission,secrets:SecretTracker,narrow:(frame:number)=>void){this.optIn=optIn;this.core=core;this.writer=writer;this.admission=admission;this.secrets=secrets;this.narrow=narrow;}
 get selected():boolean{return this.input!==undefined;}
 prepare(ctx:Context,input:InitParams):Context {
  if(!this.optIn||!input.host_services)return ctx;
  const snapshot=decodeInitParams(encodeInitParams(input)),l=snapshot.host_services!.limits;
  try{this.writer.narrow(l.max_queued_write_bytes,l.write_timeout_ms,l.max_frame_bytes);}catch{throw new InitError('invalid_init','host_services.limits');}
  const limits=this.admission.limits;
  limits.forwardSlots=Math.min(limits.forwardSlots,l.host_to_plugin_inflight);
  limits.reverseSlots=Math.min(limits.reverseSlots,l.plugin_to_host_inflight);
  limits.controlSlots=Math.min(limits.controlSlots,l.control_slots);
  this.narrow(l.max_frame_bytes);
  this.core.methodTimeoutMS=Object.freeze({...l.method_timeout_ms});
  this.core.reverseSlots=limits.reverseSlots;
  this.core.provisional=true;
  this.input=snapshot;
  return this.context(ctx);
 }
 context(ctx:Context):Context {
  const scope=requestScope(ctx);
  if(!this.input||!scope||!scope.binding||typeof scope.request.id!=='number'||scope.request.id<=0||['hook/handle','hook/handle_batch'].includes(scope.request.method)||!this.active&&scope.request.method!=='plugin/init')return ctx;
  if(['plugin/init','plugin/load','plugin/unload'].includes(scope.request.method)&&(!this.input.host_services!.limits.method_timeout_ms['host/log']||!this.input.grants.some(g=>g.name==='log.write'&&g.schema_version===1)))return ctx;
  return hostClientContext(ctx,this.core,this.input,this.secrets);
 }
 acceptedGrants(grants:InitParams['grants']):void{if(this.input)this.input.grants=cloneGrantSet(grants);}
 activate(id:number):void{if(this.input){this.core.activate(id);this.active=true;}}
 decline():void{if(this.input&&!this.active){this.input=undefined;this.core.revokeReverse();}}
}
