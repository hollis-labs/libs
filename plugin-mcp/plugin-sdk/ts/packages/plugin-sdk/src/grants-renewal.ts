import { decodeGrantSet, decodeRuntimeIdentity, cloneGrantSet, grantScopeJSON, grantTimestamp } from './init-contract.js';
import { decodeJSONObject, parseJSONTokens } from './strict-json.js';
import { decodeForwardContext } from './host-rpc.js';
import {requestScope} from './admission.js';
import type { Context, ServerPlugin } from './types.js';
import type { Grant, GrantsRenewParams, GrantsRenewResult, RuntimeIdentity } from './wire.js';

const states = new WeakMap<Context,GrantState>();
const clone = cloneGrantSet;
export function currentGrantSet(context:Context):Grant[] | undefined { return states.get(context)?.snapshot(); }
export function grantContext(context:Context,state:GrantState):Context { states.set(context,state);return context; }
export function grantState(context:Context):GrantState | undefined { return states.get(context); }
export function decodeGrantsRenewParams(raw:string):GrantsRenewParams {
 const names=['renewal_version','sequence','incarnation','grants','context'];
 const f=decodeJSONObject(raw,names);
 if(f.size!==names.length||names.some(n=>!f.has(n)))throw new Error('invalid grant renewal object');
 if(f.get('renewal_version')!=='1'||!/^\d+$/.test(f.get('sequence')!))throw new Error('invalid renewal version/sequence');
 const seq=Number(f.get('sequence'));if(!Number.isSafeInteger(seq)||seq<=0)throw new Error('invalid renewal sequence');
 const owner=decodeRuntimeIdentity(f.get('incarnation')!);decodeGrantSet(f.get('grants')!,owner);
 const ctx=decodeForwardContext(f.get('context')!);if(ctx.binding_id!==undefined)throw new Error('grant renewal cannot borrow a binding');
 const params=parseJSONTokens(raw) as GrantsRenewParams;params.grants=decodeGrantSet(f.get('grants')!,owner);return params;
}
// Authority remains host owned. This state replaces SDK discovery only.
export class GrantState {
 private grants?:Grant[];private owner?:RuntimeIdentity;private sequence=0;private busy=false;private ended=false;private enabled=false;
 activate(owner:RuntimeIdentity,grants:Grant[],enabled=true):void {this.enabled=enabled;this.owner={...owner};this.grants=clone(grants);}
 end():void{this.ended=true;}
 get closed():boolean{return this.ended;}
 snapshot():Grant[]|undefined{return this.ended||!this.grants?undefined:clone(this.grants);}
 private validate(next:GrantsRenewParams):number {
  if(this.ended||!this.grants||!this.owner||this.owner.host_instance!==next.incarnation.host_instance||this.owner.owner_id!==next.incarnation.owner_id||this.owner.owner_generation!==next.incarnation.owner_generation||this.grants.length!==next.grants.length)throw new Error('grant renewal changed authority or ended');
  let end=Date.now()+next.context.timeout_ms;
  for(let i=0;i<this.grants.length;i++){
   const old=this.grants[i]!,n=next.grants[i]!;
   const oldEnd=grantTimestamp(old.expires_at),newEnd=grantTimestamp(n.expires_at),start=grantTimestamp(n.issued_at),now=BigInt(Date.now())*1000000n;
   if(now>=oldEnd||start<grantTimestamp(old.issued_at)||newEnd<=oldEnd)throw new Error('grant renewal requires a live advancing lease');
   const {issued_at:_oi,expires_at:_oe,...a}=old,{issued_at:_ni,expires_at:_ne,...b}=n;
   if(['grant_id','name','schema_version','host_instance','owner_id','owner_generation','audience','policy_revision'].some(key=>a[key as keyof typeof a]!==b[key as keyof typeof b])||grantScopeJSON(old)!==grantScopeJSON(n))throw new Error('grant renewal changed authority');
   end=Math.min(end,Number(oldEnd/1000000n));
  }
  return end;
 }
 async renew(context:Context,next:GrantsRenewParams,plugin:ServerPlugin):Promise<GrantsRenewResult>{
  if(!this.enabled||!plugin.grantsRenewed)throw new Error('grant renewal not supported');
  if(this.busy||next.sequence!==this.sequence+1||context.signal.aborted)throw new Error('grant renewal replay or busy');
  const end=this.validate(next);this.busy=true;this.sequence=next.sequence;
  const remaining=Math.min(end-Date.now(),(requestScope(context)?.deadline??Infinity)-performance.now());
  if(remaining<=0){this.busy=false;this.end();throw new Error('grant renewal budget expired');}
  const timer=AbortSignal.timeout(Math.max(1,Math.floor(remaining)));const signal=AbortSignal.any([context.signal,timer]);
  try{
   await plugin.grantsRenewed(grantContext({...context,signal},this),clone(next.grants));
   if(signal.aborted)throw signal.reason;
   this.validate(next);this.grants=clone(next.grants);
   return {renewal_version:1,sequence:next.sequence,incarnation:{...next.incarnation}};
  }catch(error){this.end();throw error;}finally{this.busy=false;}
 }
}

export function decodeGrantsRenewResult(raw:string):GrantsRenewResult {
 const f=decodeJSONObject(raw,['renewal_version','sequence','incarnation']);
 if(f.get('renewal_version')!=='1'||!/^\d+$/.test(f.get('sequence')!))throw new Error('invalid grant renewal ack');
 const seq=Number(f.get('sequence'));if(!Number.isSafeInteger(seq)||seq<=0)throw new Error('invalid grant renewal ack sequence');
 decodeRuntimeIdentity(f.get('incarnation')!);return parseJSONTokens(raw) as GrantsRenewResult;
}
