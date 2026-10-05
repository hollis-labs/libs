import { PublicationFullError } from "./publication.js";
import { requestScope, requestBudget } from "./admission.js";
import type { Context } from "./types.js";
import { DeadlineExceededError, TransportCancelledError, RPCTransportError, decodeRPCControlDTO } from "./request-control.js";
import { decodeHostRPCDTO } from './host-rpc.js';
import type { HostRPCDTOs } from './host-rpc.js';
import { inspectEnvelope, parseJSONTokens, validatePortableJSON } from './strict-json.js';
import type { RPCID } from './wire.js';
export class CorrelationError extends Error {
    constructor() { super('invalid correlation'); this.name = 'CorrelationError'; }
}
export const CORE_CAPACITY = 256;
type DTO = keyof HostRPCDTOs;
const methods: Record<string, [
    DTO,
    DTO
]> = {
    'host/storage/get': ['StorageGetParams', 'StorageGetResult'], 'host/storage/put': ['StoragePutParams', 'StoragePutResult'], 'host/storage/delete': ['StorageDeleteParams', 'StorageDeleteResult'], 'host/secrets/get': ['SecretsGetParams', 'SecretsGetResult'], 'host/egress/request': ['EgressRequestParams', 'EgressRequestResult'], 'host/events/publish': ['EventsPublishParams', 'EventsPublishResult'], 'host/log': ['LogParams', 'LogResult'], 'host/readonly/query': ['ReadonlyQueryParams', 'ReadonlyQueryResult'], 'host/mcp/list_tools': ['MCPListToolsParams', 'MCPListToolsResult'], 'host/mcp/call_tool': ['MCPCallToolParams', 'MCPCallToolResult'], 'host/mcp/cancel_call': ['MCPCancelCallParams', 'MCPCancelCallResult'], 'host/bindings/renew': ['BindingsRenewParams', 'BindingsRenewResult'],
};
export interface CallMetadata { id?:number; pending?:object; receivedAt?:number; receivedWall?:number; published?:boolean; }
type Pending = {
    method:string; metadata:CallMetadata;
    dto: DTO;
    resolve: (result: unknown) => void;
    reject: (error: unknown) => void;
    cleanup:()=>void;
    failure:(cause:unknown)=>unknown;
    deadline:number;expire:()=>void;revoke:()=>void;
};
/** Internal engine, intentionally absent from the package's author exports. */
export class Correlation {
    readonly incoming = new Set<RPCID>();
    private pending = new Map<number, Pending>();
    private next = 0;
    private high = 0;
    private failure: unknown;
    publish!: (frame: string) => Promise<void>;
    publishCall?: (frame:string,signal:AbortSignal,onStart:()=>void,beforeStart:()=>unknown,prepare:()=>string)=>Promise<void>;
    publishControl?: (frame:string)=>Promise<void>;
    reverseSlots=8;
    methodTimeoutMS:Record<string,number>=Object.create(null);
    encode!: (value: unknown) => string;
    directional: boolean;
    provisional=false;
    get readsReplies():boolean{return !this.failure&&(this.directional||this.provisional);}
    activate(id:number):void{this.directional=true;this.provisional=false;this.high=id;}
    revokeReverse():void{this.provisional=false;for(const entry of [...this.pending.values()])entry.revoke();}
    constructor(directional = false) { this.directional = directional; }
    admit(id: RPCID | undefined): void {
        if (this.failure)
            throw this.failure;
        if (id === undefined)
            return;
        if (this.incoming.has(id) || this.incoming.size >= CORE_CAPACITY)
            throw new CorrelationError();
        if (this.directional) {
            if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= this.high || id <= 0)
                throw new CorrelationError();
            this.high = id;
        }
        this.incoming.add(id);
    }
    release(id: RPCID | null | undefined): void { if (id !== null && id !== undefined)
        this.incoming.delete(id); }
    close(error: unknown = new Error('connection closed')): void {
        if (this.failure)
            return;
        this.failure = error;
        for (const entry of this.pending.values())
            {entry.cleanup();entry.reject(entry.failure(error));}
        this.pending.clear();
        this.incoming.clear();
    }
    call(method:string,paramsRaw:string,context?:Context):Promise<unknown> {return this.callTracked(method,paramsRaw,context,{});}
    ownsCall(metadata:CallMetadata):boolean {const entry=metadata.id===undefined?undefined:this.pending.get(metadata.id);return metadata.id!==undefined&&metadata.pending!==undefined&&(!entry&&metadata.published===true||entry===metadata.pending&&entry.method==='host/mcp/call_tool');}
    callTracked(method:string,paramsRaw:string,context:Context|undefined,metadata:CallMetadata):Promise<unknown> {
      const started=performance.now();
      const pair=Object.hasOwn(methods,method)?methods[method]:undefined;
      const ceiling=this.methodTimeoutMS[method];
      if(!pair||!this.readsReplies||this.failure||!Number.isSafeInteger(ceiling)||ceiling<=0||this.next===Number.MAX_SAFE_INTEGER)return Promise.reject(this.failure??new CorrelationError());
      if(this.pending.size>=this.reverseSlots)return Promise.reject({code:'rate_limited',effect_state:'not_started',retryable:false});
      let params:Record<string,unknown>,reverse:{binding_id:string;timeout_ms:number;parent_call:{request_owner:string;id:number}};
      try{params=decodeHostRPCDTO(pair[0],paramsRaw) as unknown as Record<string,unknown>;reverse=params.context as typeof reverse;}catch(error){return Promise.reject(error);}
      const scope=context&&requestScope(context);
      const budget=context&&requestBudget(context);
      if(scope&&(typeof scope.request.id!=='number'||reverse.parent_call.request_owner!=='host'||reverse.parent_call.id!==scope.request.id||budget?.binding&&budget.binding!==reverse.binding_id))return Promise.reject(new Error('parent_invalid'));
      let remaining=Math.min(ceiling,reverse.timeout_ms);
      if(budget?.deadline!==undefined)remaining=Math.min(remaining,budget.deadline-started);
      const end=started+remaining;
      if(context?.signal.aborted||end<=performance.now())return Promise.reject(context?.signal.reason??new DeadlineExceededError());
      remaining=Math.floor(end-performance.now());if(remaining<1)return Promise.reject(new DeadlineExceededError());
      params={...params,context:{...reverse,timeout_ms:remaining}};
      const id=++this.next;
      return new Promise((resolve,reject)=>{
       let timer:ReturnType<typeof setTimeout>|undefined,possible=false,completed=false;
       const publication=new AbortController();
       const cleanup=()=>{completed=true;if(timer!==undefined)clearTimeout(timer);context?.signal.removeEventListener('abort',cancel);};
       const mutation=['host/storage/put','host/storage/delete','host/events/publish','host/egress/request','host/mcp/call_tool','host/mcp/cancel_call','host/bindings/renew'].includes(method);
       const failure=(cause:unknown)=>new RPCTransportError(possible&&mutation?'unknown_outcome':cause instanceof DeadlineExceededError?'deadline_exceeded':cause instanceof TransportCancelledError?'cancelled':cause instanceof PublicationFullError?'rate_limited':'target_unavailable',possible?'unknown':'not_started',id,cause);
       const entry:Pending={method,metadata,dto:pair[1],resolve,reject,cleanup,failure,deadline:end,expire:()=>cancel(),revoke:()=>cancel()};this.pending.set(id,entry);metadata.id=id;metadata.pending=entry;
       const fail=(error:unknown)=>{if(this.pending.get(id)===entry){this.pending.delete(id);cleanup();reject(error);}};
       const cancel=()=>{
        if(completed||this.pending.get(id)!==entry)return;
        const deadline=performance.now()>=end;
        const cause=deadline?new DeadlineExceededError():new TransportCancelledError(scope?'parent_cancelled':'caller_cancelled');
        fail(new RPCTransportError(possible&&mutation?'unknown_outcome':deadline?'deadline_exceeded':'cancelled',possible?'unknown':'not_started',id,cause));
        publication.abort(cause);
        if(possible&&this.publishControl){
         const reason=scope&&context?.signal.aborted?'parent_cancelled':cause instanceof TransportCancelledError?cause.reason:deadline?'deadline_exceeded':'caller_cancelled';
         try{void this.publishControl(this.encode({jsonrpc:'2.0',method:'rpc/cancel',params:{request_owner:'plugin',id,reason}})).catch(error=>this.close(error));}catch(error){this.close(error);}
        }
       };
       context?.signal.addEventListener('abort',cancel,{once:true});
       const tick=()=>{if(completed)return;const ms=end-performance.now();if(ms<=0)cancel();else timer=setTimeout(tick,Math.min(ms,2147483647));};
       try{
        const frame=this.encode({jsonrpc:'2.0',id,method,params});
        if(end<=performance.now()||context?.signal.aborted){cancel();return;}
        const receipt=this.publishCall?this.publishCall(frame,publication.signal,()=>{possible=true;metadata.published=true;},()=>performance.now()>=end?new DeadlineExceededError():undefined,()=>{
          const remaining=Math.floor(end-performance.now());if(remaining<1)throw new DeadlineExceededError();
          return this.encode({jsonrpc:'2.0',id,method,params:{...params,context:{...reverse,timeout_ms:remaining}}});
        }):(possible=true,metadata.published=true,this.publish(frame));
        void receipt.catch(error=>fail(failure(error)));
        tick();
       }catch(error){fail(error);}
      });
    }
    reply(line: string): void {
        const receivedAt=performance.now(),receivedWall=Date.now();
        validatePortableJSON(line);
        const fields = inspectEnvelope(line).fields!;
        if ([...fields.keys()].some(k => !['jsonrpc', 'id', 'result', 'error'].includes(k)))
            throw new CorrelationError();
        if (fields.has('error')) {
            const raw = fields.get('error')!;
            if ((parseJSONTokens(raw) as {
                code: number;
            }).code === -32010)
                decodeHostRPCDTO('ApplicationErrorResponse', line);
            const e = inspectEnvelope(raw);
            const data=e.fields?.get('data');if(data){const contract=inspectEnvelope(data).fields?.get('contract');if(contract!==undefined&&parseJSONTokens(contract)==='plugin-rpc/2')decodeRPCControlDTO('PluginRPCErrorData',data);}
            if (e.duplicates.size || e.invalidKeys || !e.fields || [...e.fields.keys()].some(k => !['code', 'message', 'data'].includes(k)))
                throw new CorrelationError();
        }
        const id = parseJSONTokens(fields.get('id')!);
        if (typeof id !== 'number')
            return;
        const entry = this.pending.get(id);
        if (!entry)
            return;
        let value: unknown, error: unknown;
        try {
            if (fields.has('result'))
                value = decodeHostRPCDTO(entry.dto, fields.get('result')!);
            else {
                const raw = fields.get('error')!;
                validatePortableJSON(raw);
                const e = inspectEnvelope(raw);
            const data=e.fields?.get('data');if(data){const contract=inspectEnvelope(data).fields?.get('contract');if(contract!==undefined&&parseJSONTokens(contract)==='plugin-rpc/2')decodeRPCControlDTO('PluginRPCErrorData',data);}
                if (e.duplicates.size || e.invalidKeys || !e.fields || [...e.fields.keys()].some(k => !['code', 'message', 'data'].includes(k)))
                    throw new CorrelationError();
                const fault = parseJSONTokens(raw) as {
                    code: number;
                    message: string;
                };
                if (fault.code === -32010)
                    decodeHostRPCDTO('ApplicationErrorResponse', line);
                error = fault;
            }
        }
        catch {
            throw new CorrelationError();
        }
        if(performance.now()>=entry.deadline){entry.expire();return;}
        entry.metadata.receivedAt=receivedAt;entry.metadata.receivedWall=receivedWall;
        this.pending.delete(id);
        entry.cleanup();
        if (error)
            entry.reject(error);
        else
            entry.resolve(value);
    }
}
export function replyCandidate(line: string): boolean {
    try {
        const fields = inspectEnvelope(line).fields;
        return fields?.has('result') === true || fields?.has('error') === true;
    }
    catch {
        return true;
    }
}
