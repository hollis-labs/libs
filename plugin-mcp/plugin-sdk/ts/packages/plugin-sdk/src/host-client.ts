import { Buffer } from 'node:buffer';
import { Correlation } from './correlation.js';
import { linkRequestScope, requestBudget, requestScope, retainRequestWork } from './admission.js';
import { DeadlineExceededError, TransportCancelledError } from './request-control.js';
import { encodeBoundedJSON } from './frame-codec.js';
import { decodeInitParams } from './init-contract.js';
import { decodeHostRPCDTO } from './host-rpc.js';
import { parseJSONTokens } from './strict-json.js';
import type { CallMetadata } from './correlation.js';
import type { RequestScope } from './admission.js';
import type * as DTO from './host-rpc.js';
import type { Context } from './types.js';
import type { InitParams, RPCError } from './wire.js';
import type { SecretTracker } from './log.js';

const brand: unique symbol = Symbol('SDK-owned HostClient');
export type StorageGetArgs = Omit<DTO.StorageGetParams,'context'>;
export type StoragePutArgs = Omit<DTO.StoragePutParams,'context'>;
export type StorageDeleteArgs = Omit<DTO.StorageDeleteParams,'context'>;
export type SecretsGetArgs = Omit<DTO.SecretsGetParams,'context'>;
export type EgressRequestArgs = Omit<DTO.EgressRequestParams,'context'>;
export type EventsPublishArgs = Omit<DTO.EventsPublishParams,'context'>;
export type HostLogArgs = Omit<DTO.LogParams,'context'>;
export type ReadonlyQueryArgs = Omit<DTO.ReadonlyQueryParams,'context'>;
export type MCPListToolsArgs = Omit<DTO.MCPListToolsParams,'context'>;
export type MCPCallToolArgs = Omit<DTO.MCPCallToolParams,'context'>;
export type MCPCancelCallArgs = {grant_id:string;call:MCPToolCall};
export type BindingsRenewArgs = Omit<DTO.BindingsRenewParams,'context'>;
const callBrand:unique symbol=Symbol('SDK-owned MCP call');
export interface MCPToolCall {readonly [callBrand]:true;readonly result:Promise<DTO.MCPCallToolResult>;}
const calls=new WeakMap<MCPToolCall,{core:Correlation;scope:RequestScope;metadata:CallMetadata}>();
export interface HostCallOptions { readonly signal?: AbortSignal; readonly timeoutMs?: number; }
export interface SecretValue { readonly value: Uint8Array; readonly expires_at: string; }
/** SDK-owned: authors consume this client and never implement it. Later service
 * helpers extend this same type. Base/hooks-only production contexts omit it. */
export interface HostClient {
 readonly [brand]: true;
 readonlyQuery(args:ReadonlyQueryArgs,options?:HostCallOptions):Promise<DTO.ReadonlyQueryResult>;
 mcpListTools(args:MCPListToolsArgs,options?:HostCallOptions):Promise<DTO.MCPListToolsResult>;
 mcpCallTool(args:MCPCallToolArgs,options?:HostCallOptions):MCPToolCall;
 mcpCancelCall(args:MCPCancelCallArgs,options?:HostCallOptions):Promise<DTO.MCPCancelCallResult>;
 bindingsRenew(args:BindingsRenewArgs,options?:HostCallOptions):Promise<DTO.BindingsRenewResult>;
 storageGet(args:StorageGetArgs,options?:HostCallOptions):Promise<DTO.StorageGetResult>;
 storagePut(args:StoragePutArgs,options?:HostCallOptions):Promise<DTO.StoragePutResult>;
 storageDelete(args:StorageDeleteArgs,options?:HostCallOptions):Promise<DTO.StorageDeleteResult>;
 secretsGet(args:SecretsGetArgs,options?:HostCallOptions):Promise<SecretValue>;
 egressRequest(args:EgressRequestArgs,options?:HostCallOptions):Promise<DTO.EgressRequestResult>;
 eventsPublish(args:EventsPublishArgs,options?:HostCallOptions):Promise<DTO.EventsPublishResult>;
 log(args:HostLogArgs,options?:HostCallOptions):Promise<DTO.LogResult>;
}
/** Local planning failure, not a fabricated host reply or request ID. */
export class HostClientError extends Error {
 readonly effect_state: 'not_started' | 'unknown'; readonly retryable=false;
 readonly code:DTO.HostRPCErrorData['code'];
 constructor(code:DTO.HostRPCErrorData['code'],effectState:'not_started'|'unknown'='not_started') {super('host client: '+code);this.code=code;this.effect_state=effectState;this.name='HostClientError';}
}
/** Validated application fault. Discriminate by data.code/detail, never message. */
export class HostRPCFailure extends Error {
 readonly code=-32010;
 readonly data:DTO.HostRPCErrorData;
 constructor(data:DTO.HostRPCErrorData) {super('host RPC: '+data.code);this.data=data;this.name='HostRPCFailure';}
}

// Internal factory, excluded from the public barrel/package subpaths. Production
// activation waits for the negotiated reverse lifecycle slice.
export function hostClientContext(context:Context,core:Correlation,input:InitParams,secrets:SecretTracker,bindingExpiry?:number):Context {
 const init=decodeInitParams(encodeBoundedJSON(input,8*1024*1024));
 const scope=requestScope(context);
 if(!scope || !core.directional || !init.host_services || !scope.binding || !secrets)throw new HostClientError('target_unavailable');
 return {...context,host:new HostClientImpl(context,core,init,secrets,bindingExpiry)};
}
class HostClientImpl implements HostClient {
 readonly [brand]=true as const;
 readonly #context:Context; readonly #core:Correlation; readonly #secrets:SecretTracker;
 readonly #grants:Map<string,InitParams['grants'][number]>;
 readonly #ceilings:Record<string,number>;
 constructor(context:Context,core:Correlation,input:InitParams,secrets:SecretTracker,bindingExpiry?:number) {
  this.#context=context;this.#core=core;this.#secrets=secrets;
  if(bindingExpiry!==undefined){const scope=requestScope(context)!;const end=performance.now()+(bindingExpiry-Date.now());scope.lease.end=Math.min(scope.lease.end??Infinity,end);}
  this.#grants=new Map(input.grants.map(g=>[g.grant_id,g]));
  this.#ceilings=Object.freeze({...input.host_services!.limits.method_timeout_ms});
 }
 async #invoke<T>(method:string,descriptor:string,args:{grant_id:string},options:HostCallOptions|undefined,build?:(params:Record<string,unknown>)=>Record<string,unknown>,metadata:CallMetadata={},accept?:(result:T,params:Record<string,unknown>,metadata:CallMetadata)=>void):Promise<T> {
  const started=performance.now(),scope=requestScope(this.#context),budget=requestBudget(this.#context);
  if(!scope || !scope.acceptsResult() || this.#context.signal.aborted || ['hook/handle','hook/handle_batch'].includes(scope.request.method))throw new HostClientError('target_unavailable');
  if(['plugin/init','plugin/load','plugin/unload'].includes(scope.request.method)&&method!=='host/log')throw new HostClientError('target_unavailable');
  const ceiling=this.#ceilings[method];if(!Number.isSafeInteger(ceiling)||ceiling!<=0)throw new HostClientError('unsupported_capability');
  const grant=this.#grants.get(args.grant_id);if(!grant||method!=='host/bindings/renew'&&(grant.name!==descriptor||grant.schema_version!==1))throw new HostClientError('capability_denied');
  if(typeof scope.request.id!=='number'||scope.request.id<=0||!scope.binding)throw new HostClientError('target_unavailable');
  if(options?.timeoutMs!==undefined&&(!Number.isInteger(options.timeoutMs)||options.timeoutMs<1||options.timeoutMs>4294967295))throw new HostClientError('invalid_request');
  let end=Math.min(started+ceiling!,budget?.deadline??Infinity,started+(Date.parse(grant.expires_at)-Date.now()),options?.timeoutMs===undefined?Infinity:started+options.timeoutMs);
  end=Math.min(end,scope.lease.end??Infinity,scope.lease.budgetEnd??Infinity);
  if(!Number.isFinite(end)||Math.floor(end-performance.now())<1)throw new DeadlineExceededError();
  const controller=new AbortController(),detach:Array<()=>void>=[];
  for(const signal of [this.#context.signal,options?.signal])if(signal){
   const abort=()=>controller.abort(signal.reason instanceof DeadlineExceededError || signal.reason instanceof TransportCancelledError ? signal.reason : new TransportCancelledError('caller_cancelled'));signal.addEventListener('abort',abort,{once:true});detach.push(()=>signal.removeEventListener('abort',abort));if(signal.aborted)abort();
  }
  const context={...this.#context,signal:controller.signal};
  linkRequestScope(this.#context,controller.signal,{deadline:end,binding:scope.binding});
  const release=retainRequestWork(this.#context);
  try {
   if(controller.signal.aborted)throw controller.signal.reason;
   let params={...args,context:{binding_id:scope.binding,timeout_ms:Math.floor(end-performance.now()),parent_call:{request_owner:'host',id:scope.request.id}}};
   if(build)params=build(params) as typeof params;
   const raw=encodeBoundedJSON(params,1024*1024);
   if(!scope.acceptsResult())throw new HostClientError('target_unavailable');
   const result=await this.#core.callTracked(method,raw,context,metadata) as T;
   if(accept)accept(result,params,metadata);
   if(['host/storage/put','host/storage/delete','host/egress/request','host/events/publish','host/mcp/call_tool'].includes(method))return this.#receipt(result,(result as {operation_key?:string}).operation_key===(params as {operation_key?:string}).operation_key);
   return result;
  } catch(error) {
   const fault=error as RPCError;
   if(fault?.code===-32010) {
    const data=decodeHostRPCDTO('HostRPCErrorData',encodeBoundedJSON(fault.data,1024*1024));
    throw new HostRPCFailure(data);
   }
   throw error;
  } finally {release();for(const remove of detach)remove();}
 }
 #receipt<T>(result:T,valid:boolean):T {
  if(!valid){this.#core.close(new Error('invalid receipt'));throw new HostClientError('unknown_outcome','unknown');}return result;
 }
 readonlyQuery(a:ReadonlyQueryArgs,o?:HostCallOptions):Promise<DTO.ReadonlyQueryResult>{return this.#invoke('host/readonly/query','readonly.query',a,o,undefined,{},(result,params)=>{if(result.resource!==params.resource||result.schema_version!==params.schema_version){this.#core.close(new Error('invalid echo'));throw new HostClientError('target_unavailable');}});}
 mcpListTools(a:MCPListToolsArgs,o?:HostCallOptions):Promise<DTO.MCPListToolsResult>{return this.#invoke('host/mcp/list_tools','mcp.reach',a,o,undefined,{},(result,params)=>{if(result.server_id!==params.server_id){this.#core.close(new Error('invalid echo'));throw new HostClientError('target_unavailable');}});}
 mcpCallTool(a:MCPCallToolArgs,o?:HostCallOptions):MCPToolCall {
  const metadata:CallMetadata={},scope=requestScope(this.#context);
  const result=this.#invoke<DTO.MCPCallToolResult>('host/mcp/call_tool','mcp.reach',a,o,undefined,metadata);
  const handle:MCPToolCall=Object.freeze({[callBrand]:true as const,result});
  if(scope)calls.set(handle,{core:this.#core,scope,metadata});return handle;
 }
 mcpCancelCall(a:MCPCancelCallArgs,o?:HostCallOptions):Promise<DTO.MCPCancelCallResult>{
  const ref=calls.get(a.call);if(!ref||ref.core!==this.#core||ref.scope!==requestScope(this.#context)||!this.#core.ownsCall(ref.metadata))return Promise.reject(new HostClientError('scope_denied'));
  const params={grant_id:a.grant_id,target_call_id:ref.metadata.id!};return this.#invoke('host/mcp/cancel_call','mcp.reach',params,o);
 }
 async bindingsRenew(a:BindingsRenewArgs,o?:HostCallOptions):Promise<DTO.BindingsRenewResult>{
  const scope=requestScope(this.#context);if(!scope||!scope.acceptsResult())throw new HostClientError('target_unavailable');
  if(scope.lease.renewPending)throw new HostClientError('rate_limited');scope.lease.renewPending=true;
  try{return await this.#invoke('host/bindings/renew','binding-current-grants',a,o,undefined,{},(result:DTO.BindingsRenewResult,params,metadata)=>{
   if(result.binding_id!==scope.binding){this.#core.close(new Error('invalid receipt'));throw new HostClientError('unknown_outcome','unknown');}
   if(!scope.acceptsResult()||this.#context.signal.aborted||metadata.receivedAt===undefined||metadata.receivedWall===undefined)throw new HostClientError('target_unavailable');
   const lease=scope.lease,received=metadata.receivedAt;
   lease.end=Math.min(received+Date.parse(result.expires_at)-metadata.receivedWall,received+Number(params.requested_lease_ms));
   lease.budgetEnd=Math.min(lease.budgetEnd??Infinity,received+result.remaining_budgets.timeout_ms);
   for(const key of ['bytes','effects','tokens'] as const){const value=result.remaining_budgets[key];if(value!==undefined)lease[key]=Math.min(lease[key]??Infinity,value);}
  });}finally{scope.lease.renewPending=false;}
 }
 storageGet(a:StorageGetArgs,o?:HostCallOptions):Promise<DTO.StorageGetResult>{return this.#invoke('host/storage/get','storage.read',a,o);}
 storagePut(a:StoragePutArgs,o?:HostCallOptions):Promise<DTO.StoragePutResult>{return this.#invoke('host/storage/put','storage.write',a,o);}
 storageDelete(a:StorageDeleteArgs,o?:HostCallOptions):Promise<DTO.StorageDeleteResult>{return this.#invoke('host/storage/delete','storage.write',a,o);}
 async secretsGet(a:SecretsGetArgs,o?:HostCallOptions):Promise<SecretValue>{
  const r=await this.#invoke<DTO.SecretsGetResult>('host/secrets/get','secrets.read',a,o);
  const value=Uint8Array.from(Buffer.from(r.value_base64,'base64'));
  this.#secrets.add(r.value_base64);
  try{this.#secrets.add(new TextDecoder('utf-8',{fatal:true}).decode(value));}catch{/* Binary has no exact UTF-8 string representation. */}
  return {value,expires_at:r.expires_at};
 }
 egressRequest(a:EgressRequestArgs,o?:HostCallOptions):Promise<DTO.EgressRequestResult>{return this.#invoke('host/egress/request','egress.request',a,o);}
 eventsPublish(a:EventsPublishArgs,o?:HostCallOptions):Promise<DTO.EventsPublishResult>{return this.#invoke('host/events/publish','events.publish',a,o);}
 log(a:HostLogArgs,o?:HostCallOptions):Promise<DTO.LogResult>{return this.#invoke('host/log','log.write',a,o,params=>{
  // Validate the original before redaction: invalid input must not become valid.
  decodeHostRPCDTO('LogParams',encodeBoundedJSON(params,1024*1024));
  let values=4096;
  const walk=(value:unknown,depth=0):unknown=>{
   if(--values<0||depth>128)throw new Error('log value budget');
   if(typeof value==='string')return this.#secrets.redact(value);
   if(Array.isArray(value))return value.map(v=>walk(v,depth+1));
   if(value&&typeof value==='object')return Object.fromEntries(Object.entries(value).map(([k,v])=>[this.#secrets.redact(k),walk(v,depth+1)]));
   return value;
  };
  const fields=a.fields?.map(f=>{let value:unknown;try{value=walk(parseJSONTokens(encodeBoundedJSON(f.value,64*1024)));}catch{value='[REDACTED]';}return {name:this.#secrets.redact(f.name),value};});
  return {...params,message:this.#secrets.redact(a.message),...(fields===undefined?{}:{fields})};
 });}
}
