import { decodeInitParams } from "./init-contract.js";
import { requestParamsJSON } from "./payload.js";
import { Buffer } from "node:buffer";
import type { Context } from './types.js';
import type { RPCID, RPCRequest, RPCResponse } from './wire.js';
import { FrameWriter, TerminalCredit, PublicationFullError } from './publication.js';
import { Correlation } from './correlation.js';
import { decodeRuntimeParams } from './payload.js';
import { requestFailureResponse, TransportCancelledError, DeadlineExceededError } from './request-control.js';
import type { CancelParams } from './request-control.js';
export const FORWARD_HANDLER_SLOTS = 16, REVERSE_HANDLER_SLOTS = 8, CONTROL_HANDLER_SLOTS = 2;
type LocalBudget={deadline?:number;binding?:string};
const budgets=new WeakMap<object,LocalBudget>();
export function requestBudget(ctx:Context):LocalBudget|undefined{return budgets.get(ctx.signal);}
export interface AdmissionLimits{forwardSlots?:number;reverseSlots?:number;controlSlots?:number}
export function admissionLimits(value:AdmissionLimits={}):Required<AdmissionLimits>{
 const forwardSlots=value.forwardSlots??16,reverseSlots=value.reverseSlots??8,controlSlots=value.controlSlots??2;
 if(!Number.isSafeInteger(forwardSlots)||forwardSlots<1||forwardSlots>16||!Number.isSafeInteger(reverseSlots)||reverseSlots<1||reverseSlots>8||!Number.isSafeInteger(controlSlots)||controlSlots<1||controlSlots>2)throw new Error('admission limits must be positive and may only narrow defaults');
 return {forwardSlots,reverseSlots,controlSlots};
}
const scopes = new WeakMap<object, RequestScope>();
export function requestScope(ctx: Context): RequestScope | undefined { return scopes.get(ctx.signal); }
export function linkRequestScope(ctx:Context,signal:AbortSignal,budget?:LocalBudget):void {
 const scope=requestScope(ctx);if(scope)scopes.set(signal,scope);
 const parent=requestBudget(ctx);const deadline=budget?.deadline===undefined?parent?.deadline:Math.min(budget.deadline,parent?.deadline??Infinity);
 budgets.set(signal,{deadline,binding:budget?.binding??parent?.binding});
}
export function retainRequestWork(ctx: Context): () => void { return requestScope(ctx)?.retain() ?? (() => { }); }
export class RequestScope {
    readonly lease: {end?:number;budgetEnd?:number;renewPending:boolean;bytes?:number;effects?:number;tokens?:number} = {renewPending:false};
    readonly controller = new AbortController();
    readonly executionDone: Promise<void>;
    readonly terminalDone: Promise<void>;
    terminalResolve!: () => void;
    private executionResolve!: () => void;
    private terminal = false;
    private completed = false;
    private children = 0;
    private released = false;
    private started = false;
    readonly deadline?: number;
    readonly binding?: string;
    readonly received: number;
    private timer?: ReturnType<typeof setTimeout>;
    private detach: () => void;
    readonly manager: Admission;
    readonly request: RPCRequest;
    readonly control: boolean;
    readonly credit: TerminalCredit | undefined;
    readonly context: Context;
    constructor(manager: Admission, request: RPCRequest, control: boolean, credit: TerminalCredit | undefined, context: Context, received: number) {
        this.manager = manager;
        this.request = request;
        this.control = control;
        this.credit = credit;
        this.context = context;
        this.received = received;
        this.terminalDone = new Promise(resolve => { this.terminalResolve = resolve; });
        this.executionDone = new Promise(resolve => { this.executionResolve = resolve; });
        const cancel = () => this.controller.abort(context.signal.reason);
        context.signal.addEventListener('abort', cancel, { once: true });
        this.detach = () => context.signal.removeEventListener('abort', cancel);
        if (context.signal.aborted)
            cancel();
        scopes.set(this.controller.signal, this);
        this.controller.signal.addEventListener('abort', () => {
            const reason = this.controller.signal.reason;
            if (reason instanceof TransportCancelledError || reason instanceof DeadlineExceededError)
                this.logicalReply(requestFailureResponse(request.id!, this.started&&forwardMutationMethod(request.method)?'unknown_outcome':reason instanceof DeadlineExceededError ? 'deadline_exceeded' : 'cancelled', this.started ? 'unknown' : 'not_started'));
        });
        try {
            const params = request.method === 'plugin/init' ? decodeInitParams(requestParamsJSON(request) ?? 'null') : decodeRuntimeParams<{
                context?: {
                    timeout_ms: number;
                    binding_id?: string;
                };
            }>(request);
            if (params?.context) {
                this.binding = params.context.binding_id;
                const end = received + params.context.timeout_ms;
                this.deadline = end;
 budgets.set(this.controller.signal,{deadline:end,binding:this.binding});
                const tick = () => { if(this.terminal)return; const ms = end - performance.now(); if (ms <= 0)
                    this.controller.abort(new DeadlineExceededError());
                else
                    this.timer = setTimeout(tick, Math.min(ms, 2147483647)); };
                queueMicrotask(tick);
            }
        }
        catch { /* Dispatch owns structural diagnostics. */ }
    }
    acceptsResult():boolean {
        if(this.deadline!==undefined&&performance.now()>=this.deadline)this.controller.abort(new DeadlineExceededError());
        const cause=this.controller.signal.reason;
        return !this.terminal&&!(cause instanceof TransportCancelledError)&&!(cause instanceof DeadlineExceededError);
    }
    get ctx(): Context { return { ...this.context, signal: this.controller.signal }; }
    start(): boolean { if (this.deadline !== undefined && performance.now() >= this.deadline)
        this.controller.abort(new DeadlineExceededError()); if (this.terminal)
        return false; this.started = true; return true; }
    reply(response: RPCResponse): void { this.logicalReply(response, true); }
    private logicalReply(response: RPCResponse, completed = false): void {
        if (this.terminal) { if (completed) this.finish(); return; }
        this.terminal = true;
        if (this.timer !== undefined)
            clearTimeout(this.timer);
        if (this.request.id === undefined) { if (completed) this.finish(); return; }
        const cause = this.controller.signal.reason;
        if (this.deadline !== undefined && performance.now() >= this.deadline)
            response = requestFailureResponse(this.request.id, response.error&&(response.error.data as {code?:string}|undefined)?.code==='unknown_outcome'?'unknown_outcome':'deadline_exceeded', response.error ? (response.error.data as {
                effect_state?: 'not_started' | 'not_committed' | 'committed' | 'unknown';
            } | undefined)?.effect_state ?? 'unknown' : 'committed');
        else if (cause instanceof TransportCancelledError || cause instanceof DeadlineExceededError)
            response = requestFailureResponse(this.request.id, response.error&&(response.error.data as {code?:string}|undefined)?.code==='unknown_outcome'?'unknown_outcome':cause instanceof DeadlineExceededError ? 'deadline_exceeded' : 'cancelled', response.error ? (response.error.data as {
                effect_state?: 'not_started' | 'not_committed' | 'committed' | 'unknown';
            } | undefined)?.effect_state ?? 'unknown' : 'committed');
        if (completed) this.finish();
        this.manager.publish(this, response);
    }
    finish(): void { this.completed = true; if (!this.children)
        this.release(); }
    retain(): () => void { this.children++; let done = false; return () => { if (done)
        return; done = true; this.children--; if (this.completed && !this.children)
        this.release(); }; }
    private release(): void { if (this.released)
        return; this.released = true; this.detach(); this.controller.abort(new TransportCancelledError('parent_cancelled')); this.manager.releasePermit(this.control); this.executionResolve(); }
}
export class Admission {
    private ordinary = 0;
    private control = 0;
    private active = new Map<RPCID, RequestScope>();
    readonly limits:Required<AdmissionLimits>;
    readonly writer: FrameWriter;
    readonly core: Correlation;
    readonly encode: (response: RPCResponse) => string;
    readonly fence: (error: unknown) => void;
    constructor(writer: FrameWriter, core: Correlation, encode: (response: RPCResponse) => string, fence: (error: unknown) => void,limits:AdmissionLimits={}) {this.limits=admissionLimits(limits); this.writer = writer; this.core = core; this.encode = encode; this.fence = fence; }
    begin(context: Context, request: RPCRequest, received: number): RequestScope | undefined {
        const control = ['plugin/init', 'plugin/load', 'plugin/unload'].includes(request.method);
        if (control ? this.control >= this.limits.controlSlots : this.ordinary >= this.limits.forwardSlots)
            return;
        let credit: TerminalCredit | undefined;
        if (request.id !== undefined) {
            try {
                if (Buffer.byteLength(this.encode(requestFailureResponse(request.id, 'budget_exceeded', 'committed'))) > 1024)
                    return;
                credit = this.writer.reserveTerminal();
            }
            catch {
                return;
            }
        }
        if (control)
            this.control++;
        else
            this.ordinary++;
        const scope = new RequestScope(this, request, control, credit, context, received);
        if (request.id !== undefined)
            this.active.set(request.id, scope);
        return scope;
    }
    releasePermit(control: boolean): void { if (control)
        this.control--;
    else
        this.ordinary--; }
    cancel(params: CancelParams): void { if (params.request_owner === 'host')
        this.active.get(params.id)?.controller.abort(new TransportCancelledError(params.reason)); }
    publish(scope: RequestScope, response: RPCResponse): void {
        const receipt = () => { if (this.active.get(scope.request.id!) === scope) {
            this.active.delete(scope.request.id!);
            this.core.release(scope.request.id);
        } scope.terminalResolve(); };
        const send = (value: RPCResponse) => this.writer.publish(this.encode(value), 'control', scope.credit);
        // Capacity rejection is synchronous in the writer; failed replacement keeps
        // the terminal reservation available for a bounded classified response.
        let promise: Promise<void>;
        try {
            promise = send(response);
        }
        catch (error) {
            try {
                promise = send(requestFailureResponse(scope.request.id!, 'budget_exceeded', response.error ? (response.error.data as {
                    effect_state?: string;
                } | undefined)?.effect_state === 'committed' ? 'committed' : 'unknown' : 'committed'));
            }
            catch (failure) {
                scope.credit?.release();
                receipt();
                this.fence(failure);
                return;
            }
        }
        void promise.catch(error => { if (error instanceof PublicationFullError && scope.credit?.state === 'reserved')
            return send(requestFailureResponse(scope.request.id!, 'budget_exceeded', response.error ? (response.error.data as {
                effect_state?: string;
            } | undefined)?.effect_state === 'committed' ? 'committed' : 'unknown' : 'committed')); throw error; }).then(receipt, error => { scope.credit?.release(); receipt(); this.fence(error); });
    }
}

function forwardMutationMethod(method:string):boolean{return ['plugin/init','plugin/load','plugin/unload','command/execute','event/handle','crud/create','crud/update','crud/delete','mcp/call_tool','http/handle','plugin/migrate','hook/handle','hook/handle_batch'].includes(method);}
