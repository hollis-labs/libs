import { retainRequestWork, linkRequestScope, requestScope } from "./admission.js";
import { preserveFrameJSON,encodeBoundedJSON,DEFAULT_FRAME_BYTES } from './frame-codec.js';
import { parseJSONTokens } from './strict-json.js';
import { requestParamsJSON } from './payload.js';
import { decodeHookHandleParams, decodeHookHandleBatchParams, decodeHookHandleResult, encodeHookHandleResult, encodeHookHandleBatchResult, validateHookRequest, validateHookBatchRequest, validateHookResultFor, hookRPCError, HookValidationError } from './hooks.js';
import type { HookRequest, HookResult } from './hooks.js';
import type { HookFailureCode } from './hooks-wire.js';
import type { Context, ServerPlugin } from './types.js';
import type { RPCRequest, RPCResponse, RuntimeIdentity } from './wire.js';
const rawResponses = new WeakMap<object,string>();
/** Internal transport encoder, preserving opaque result literals. */
export function hookResponseJSON(response: RPCResponse,limit=DEFAULT_FRAME_BYTES-1): string | undefined {
 const raw=rawResponses.get(response);return raw===undefined?undefined:encodeBoundedJSON({...response,result:preserveFrameJSON(raw)},limit);
}
function failure(p: HookRequest,code: HookFailureCode,status: 'failed'|'unavailable' = 'failed'): HookResult { return {invocation_id:p.invocation_id,status,error:{code}}; }
function lease(ctx: Context,p: HookRequest,started: number): {ctx: Context; close:()=>void; expired:()=>boolean} {
  const controller=new AbortController();
  const cancel=()=>controller.abort(ctx.signal.reason);
  ctx.signal.addEventListener('abort',cancel,{once:true});if(ctx.signal.aborted) cancel();
  // Both relative snapshots start before any batch item is queued. No clock
  // synchronization is assumed; absolute deadline is diagnostic metadata.
  const ms=Math.min(p.context.timeout_ms,p.aggregate_budget_ms);
  let timer: ReturnType<typeof setTimeout>;
  const tick=()=>{const remaining=ms-(performance.now()-started);if(remaining<=0){controller.abort(new Error('deadline'));}else timer=setTimeout(tick,Math.min(remaining,2147483647));};
  tick();
  const expired=()=>{if(performance.now()-started>=ms){clearTimeout(timer);controller.abort(new Error('deadline'));}return controller.signal.aborted;};
  linkRequestScope(ctx,controller.signal,{deadline:started+ms,binding:p.context.binding_id});
  return {expired,ctx:{...ctx,signal:controller.signal,forwardContext:p.context},close:()=>{clearTimeout(timer);ctx.signal.removeEventListener('abort',cancel);}};
}
async function invoke(ctx: Context,plugin: ServerPlugin,p: HookRequest): Promise<HookResult> {
  if(!plugin.hookHandle) return failure(p,'profile_unavailable','unavailable');
  let result: HookResult;
  const release=retainRequestWork(ctx);
  try {result=await plugin.hookHandle(ctx,p);}
  catch(error){return failure(p,error instanceof Error?'handler_error':'handler_panic');}
  finally {release();}
  try {validateHookResultFor(p,result);return decodeHookHandleResult(encodeHookHandleResult(result));}
  catch{return failure(p,'invalid_output');}
}
export async function dispatchHook(plugin: ServerPlugin,ctx: Context,request: RPCRequest,incarnation: RuntimeIdentity | undefined,enabled: boolean): Promise<RPCResponse | undefined> {
  const received=requestScope(ctx)?.received??performance.now();
  const notification=request.id===undefined;
  const reject=(code: number,cause: 'invalid_params'|'profile_unavailable'|'method_not_found'|'invalid_request',field?: string): RPCResponse | undefined=>{
    const error=hookRPCError(code,cause,field);
    if(notification){ctx.logger.warn('hook notification rejected',{code:cause,...(field?{field}:{})});return undefined;}
    return {jsonrpc:'2.0',id:request.id!,error};
  };
  if(!enabled) return reject(-32601,'profile_unavailable');
  if(!plugin.hookHandle) return reject(-32601,'method_not_found');
  if(!notification && (typeof request.id!=='number'||!Number.isSafeInteger(request.id)||request.id<=0)) return reject(-32600,'invalid_request');
  let items: HookRequest[];
  try {
    const raw=requestParamsJSON(request);if(raw===undefined) throw new HookValidationError('params','required params');
    if(request.method==='hook/handle'){const p=decodeHookHandleParams(raw);validateHookRequest(p,notification);items=[p];}
    else {const p=decodeHookHandleBatchParams(raw);validateHookBatchRequest(p,notification);items=p.items;}
  }catch(error){return reject(-32602,'invalid_params','params');}
  for(const p of items) {
    Object.freeze(p.context);Object.freeze(p.scope.incarnation);Object.freeze(p.scope);
    Object.freeze(p.trace);Object.freeze(p.metadata);Object.freeze(p);
  }
  const leases=items.map(p=>lease(ctx,p,received));
  const results: HookResult[]=[];
  try {
    for(let i=0;i<items.length;i++) {
      const p=items[i]!,l=leases[i]!;
      let result: HookResult;
      const r=p.scope.incarnation;
      if(!incarnation||r.host_instance!==incarnation.host_instance||r.owner_id!==incarnation.owner_id||r.owner_generation!==incarnation.owner_generation) {result=failure(p,'stale_scope','unavailable');}
      else 
      if(l.expired()) result=failure(p,ctx.signal.aborted?'caller_cancelled':'deadline_exceeded');
      else {
        let cancel!:()=>void;
        const cancelled=new Promise<HookResult>(resolve=>{cancel=()=>resolve(failure(p,ctx.signal.aborted?'caller_cancelled':'deadline_exceeded'));l.ctx.signal.addEventListener('abort',cancel,{once:true});});
        try { result=await Promise.race([invoke(l.ctx,plugin,p),cancelled]);if(l.expired())result=failure(p,ctx.signal.aborted?'caller_cancelled':'deadline_exceeded'); }
        finally { l.ctx.signal.removeEventListener('abort',cancel); }
      }
      results.push(result);
      if(notification&&result.status!=='ok')ctx.logger.warn('hook notification failed',{status:result.status});
    }
  } finally {leases.forEach(l=>l.close());}
  if(notification) return undefined;
  const raw=request.method==='hook/handle'?encodeHookHandleResult(results[0]!):encodeHookHandleBatchResult({items:results});
  const response: RPCResponse={jsonrpc:'2.0',id:request.id!,result:parseJSONTokens(raw)};
  rawResponses.set(response,raw);
  return response;
}
