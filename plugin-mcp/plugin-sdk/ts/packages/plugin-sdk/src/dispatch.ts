import { requestScope } from "./admission.js";
import { requestFailureResponse } from "./request-control.js";
import { dispatchHook } from './hooks-dispatch.js';
import { hooksFixtureEnabled } from './hooks-fixture.js';
import { encodeBoundedJSON, DEFAULT_FRAME_BYTES, FrameTooLargeError } from './frame-codec.js';
import { PayloadError, decodeRuntimeParams, rememberParams, authoredResult, validateRuntimeResult } from './payload.js';
import { inspectEnvelope } from './strict-json.js';
import { decodeEnvelope } from './envelope.js';
import { decodeInitParams, decodeInitResult, encodeInitResult, validateInitResult, InitError } from './init-contract.js';
import { validateJSON } from './strict-json.js';
import { ConfigReader } from './config.js';
import { pluginError } from './errors.js';
import type { SecretTracker } from './log.js';
import type { Context, ServerPlugin } from './types.js';
import type * as Wire from './wire.js';

export class RPCFault extends Error {
  readonly code: number;
  constructor(code: number, message: string) { super(message); this.code = code; }
}
const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
const rawInit = new WeakMap<object,string>();
export function decodeRequest(line: string): Wire.RPCRequest | undefined {
  const request = decodeEnvelope(line);
  if(request) { rememberParams(request,line); if(request.method === 'plugin/init') rawInit.set(request,line); }
  return request;
}
const notImplemented = (name: string): never => { throw new RPCFault(-32601, `plugin does not implement ${name}`); };
function optionalObject<T extends object>(result: T, keys: Array<keyof T>): Partial<T> {
  return Object.fromEntries(keys.flatMap(key => {
    const value = result[key];
    const empty = value == null || value === false || value === '' || (Array.isArray(value) && !value.length) || (record(value) && !Object.keys(value).length);
    return empty ? [] : [[key, value]];
  })) as Partial<T>;
}
function envelopes(value: Wire.EnvelopeOut[] | null | undefined): Wire.EnvelopeOut[] | undefined {
  if (!value?.length) return undefined;
  return value.map(e => ({ type: e.type ?? '', data: e.data ?? null, ...(e.session_id ? {session_id: e.session_id} : {}) }));
}
export class Dispatcher {
  readonly plugin: ServerPlugin;
  private readonly secrets: SecretTracker;
  private readonly outputLimit: number;
  context: Context;
  get ready(): boolean { return this.initialized; }
  private attempted = false;
  private initialized = false;
  private hooksEnabled = false;
  private hookIncarnation?: Wire.RuntimeIdentity;
  private unloadAttempt?: Promise<void>;
  constructor(plugin: ServerPlugin, context: Context, secrets: SecretTracker, outputLimit = DEFAULT_FRAME_BYTES) { this.outputLimit = outputLimit; this.plugin = plugin; this.context = context; this.secrets = secrets; }
  private async identity(value: unknown, ctx: Context = this.context): Promise<void> { if (value !== undefined) await this.plugin.identity?.(ctx, value); }
  async dispatch(req: Wire.RPCRequest, requestContext: Context = this.context): Promise<Wire.RPCResponse | undefined> {
    const id = req.id;
    try {
      if((req.method==='hook/handle'||req.method==='hook/handle_batch') && this.initialized) return dispatchHook(this.plugin,requestContext,req,this.hookIncarnation,this.hooksEnabled || hooksFixtureEnabled(this.plugin));
      const result = await this.call(req,requestContext);
      if(req.method !== "plugin/init") validateRuntimeResult(req.method,result,this.outputLimit - 1);
      return id === undefined ? undefined : { jsonrpc: '2.0', id, result };
    } catch (error) {
      if (id === undefined) return undefined;
      if(error instanceof FrameTooLargeError)return requestFailureResponse(id,'budget_exceeded','committed');
      const fault = error instanceof FrameTooLargeError ? {code:-32603,message:'outbound response rejected'} : error instanceof InitError ? {code:-32602,message:error.message,data:error.rpcData()} : error instanceof PayloadError ? {code:-32602,message:error.message} : error instanceof RPCFault ? {code: error.code, message: error.message} : pluginError(error);
      return {jsonrpc: '2.0', id, error: fault};
    }
  }
  /** Record the attempt before user code; failed cleanup is never retried. */
  shutdown(context: Context): Promise<void> {
    return this.unloadAttempt ??= Promise.resolve().then(() => this.plugin.unload(context));
  }
  private async call(req: Wire.RPCRequest, requestContext: Context): Promise<unknown> {
    const p = this.plugin;
    let ctx: Context = {...requestContext,config:this.context.config,forwardContext:undefined};
    if(req.method !== 'plugin/init' && !this.initialized) throw new RPCFault(-32600,'successful init required');
    let decoded: Record<string,unknown> | undefined;
    const supported = req.method === 'plugin/load' || req.method === 'plugin/unload' || req.method === 'plugin/health' || req.method === 'command/execute' && p.command || req.method === 'event/handle' && p.eventHandle || ['crud/create','crud/read','crud/update','crud/delete','crud/list'].includes(req.method) && p.create && p.read && p.update && p.delete && p.list || req.method === 'mcp/call_tool' && p.mcpCallTool || req.method === 'http/handle' && p.httpHandle || req.method === 'plugin/migrate' && p.migrate;
    if(supported) { decoded=decodeRuntimeParams<Record<string,unknown>>(req); ctx={...ctx,forwardContext:decoded!.context as Context['forwardContext']}; }
    switch (req.method) {
      case 'plugin/init': {
        if (typeof req.id !== 'number' || !Number.isSafeInteger(req.id) || req.id <= 0) throw new RPCFault(-32600,'init requires a positive safe integer id');
        if (this.attempted) throw new RPCFault(-32600,'init already attempted');
        this.attempted = true;
        let input: Wire.InitParams;
        try {
          const raw = rawInit.get(req);
          if(raw) {
            validateJSON(raw);
            // Envelope fields retain the existing envelope policy; Init parameters are closed.
            const f = inspectEnvelope(raw).fields!;
            if(req.jsonrpc !== '2.0') throw new RPCFault(-32600,'init requires JSON-RPC 2.0');
            input = decodeInitParams(f.get('params') ?? 'null');
          } else input = decodeInitParams(JSON.stringify(req.params));
        } catch(error) { if(error instanceof InitError || error instanceof RPCFault) throw error; throw new InitError('invalid_init','params'); }
        ctx = {...ctx,forwardContext:input.context,config:new ConfigReader(input.config,this.secrets)};
        this.context = {...this.context,config:ctx.config};
        // Capture the host's offer before author code can mutate its input.
        const hooksEnabled = input.hooks_profile?.hooks_profile_version === 1 && typeof p.hookHandle === 'function';
        const authored = await p.init(ctx, input);
        if(requestScope(ctx)?.acceptsResult()===false)throw ctx.signal.reason??new Error('initialization budget expired');
        encodeBoundedJSON(authored, this.outputLimit - 1);
        const {reverse_rpc_version: _reverse, hooks_profile_version: _hooks, ...base} = authored;
        const result = decodeInitResult(encodeInitResult({...base,...(hooksEnabled ? {hooks_profile_version:1 as const} : {})}));
        validateInitResult(input,result);
        await this.identity(input.identity,ctx);
        this.hooksEnabled = hooksEnabled;
        this.hookIncarnation = input.incarnation;
        this.initialized = true;
        return result;
      }
      case 'plugin/load': {
        const result = authoredResult(req.method,await p.load(ctx),this.outputLimit - 1) as unknown as Wire.LoadResult;
        return optionalObject(result, ['skipped_registrations']);
      }
      case 'plugin/unload': await this.shutdown(ctx); return {ok: true};
      case 'plugin/health': {
        if (!p.health) return {ok: true};
        const result = authoredResult(req.method,await p.health(ctx),this.outputLimit - 1) as unknown as Wire.HealthResult;
        return {ok: result.ok ?? false, ...optionalObject(result, ['message'])};
      }
      case 'command/execute': {
        if (!p.command) return notImplemented('CommandHandler');
        const input = decoded as unknown as Wire.CommandExecParams;
        await this.identity(input.identity,ctx);
        const result = authoredResult(req.method,await p.command(ctx, input),this.outputLimit - 1) as unknown as Wire.CommandExecResult;
        return {action: result.action ?? '',...optionalObject(result, ['content']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'event/handle': {
        if (!p.eventHandle) return notImplemented('EventHandler');
        const input = decoded as unknown as Wire.EventHandleParams;
        await this.identity(input.identity,ctx);
        const result = authoredResult(req.method,await p.eventHandle(ctx, input),this.outputLimit - 1) as unknown as Wire.EventHandleResult;
        return {...optionalObject(result, ['cancel','reason']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'crud/create': case 'crud/read': case 'crud/update': case 'crud/delete': case 'crud/list': {
        if (!p.create || !p.read || !p.update || !p.delete || !p.list) return notImplemented('CRUDHandler');
        const input = decoded as unknown as Wire.CRUDParams;
        if (req.method === 'crud/create') return {data: await p.create(ctx,input.resource_type,input.data ?? null)};
        if (req.method === 'crud/read') return {data: await p.read(ctx,input.resource_type,input.id ?? '')};
        if (req.method === 'crud/update') return {data: await p.update(ctx,input.resource_type,input.id ?? '',input.data ?? null)};
        if (req.method === 'crud/delete') { await p.delete(ctx,input.resource_type,input.id ?? ''); return {ok:true}; }
        return {items: await p.list(ctx,input.resource_type,input.filters ?? null) ?? []};
      }
      case 'mcp/call_tool': {
        if (!p.mcpCallTool) return notImplemented('MCPHandler');
        const input = decoded as unknown as Wire.MCPCallRequest;
        await this.identity(input.identity,ctx);
        const result = authoredResult(req.method,await p.mcpCallTool(ctx, input),this.outputLimit - 1) as unknown as Wire.MCPCallResult;
        return {content: result.content,...optionalObject(result, ['is_error']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'http/handle': {
        if (!p.httpHandle) return notImplemented('HTTPHandler');
        const input = decoded as unknown as import('./types.js').HTTPRequest;
        await this.identity(input.identity,ctx);
        const encoded = authoredResult(req.method,await p.httpHandle(ctx,input),this.outputLimit - 1);
        return {status: encoded.status, ...optionalObject(encoded, ['headers']), ...(encoded.body ? {body: encoded.body} : {})};
      }
      case 'plugin/migrate': {
        if (!p.migrate) return notImplemented('Migrator');
        const input = decoded as unknown as Wire.MigrateParams;
        await p.migrate(ctx,input.from_version,input.to_version); return {};
      }
      default: throw new RPCFault(-32601, `unknown method ${JSON.stringify(req.method)}`);
    }
  }
}
