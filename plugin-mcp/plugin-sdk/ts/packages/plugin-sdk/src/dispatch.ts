import { decodeEnvelope } from './envelope.js';
import { decodeInitParams, decodeInitResult, encodeInitResult, validateInitResult, InitError } from './init-contract.js';
import { decodeJSONObject, validateJSON } from './strict-json.js';
import { Buffer } from 'node:buffer';
import { ConfigReader } from './config.js';
import { errorMessage, pluginError } from './errors.js';
import type { SecretTracker } from './log.js';
import type { Context, ServerPlugin } from './types.js';
import type * as Wire from './wire.js';

export class RPCFault extends Error {
  readonly code: number;
  constructor(code: number, message: string) { super(message); this.code = code; }
}
type Field = 'string' | 'bool' | 'int' | 'object' | 'strings' | 'strlist' | 'host' | 'base64' | 'json';
const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value);
function field(value: unknown, kind: Field): unknown {
  if (kind === 'json') return value;
  if (value == null) return kind === 'string' ? '' : kind === 'bool' ? false : kind === 'int' ? 0 : kind === 'host' ? {version: '', protocol: 0} : null;
  if (kind === 'string' && typeof value === 'string') return value;
  if (kind === 'bool' && typeof value === 'boolean') return value;
  if (kind === 'int' && typeof value === 'number' && Number.isSafeInteger(value)) return value;
  if (kind === 'object' && record(value)) return value;
  if (kind === 'strings' && record(value)) return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, field(v, 'string')]));
  if (kind === 'strlist' && Array.isArray(value)) return value.map(v => field(v, 'string'));
  if (kind === 'host' && record(value)) return { version: field(value.version, 'string'), protocol: field(value.protocol, 'int') };
  if (kind === 'base64') {
    if (typeof value === 'string') {
      // Buffer's decoder is permissive; reject alphabet/padding Go rejects.
      const compact = value.replace(/[\r\n]/g, '');
      if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(compact)) throw new Error('invalid base64 body');
      return Buffer.from(compact, 'base64');
    }
    if (Array.isArray(value) && value.every(n => typeof n === 'number' && Number.isInteger(n) && n >= 0 && n <= 255)) return Uint8Array.from(value);
  }
  throw new Error(`expected ${kind}`);
}
function params<T>(raw: unknown, fields: Record<string, Field>): T {
  try {
    if (raw != null && !record(raw)) throw new Error('expected object');
    const source = raw ?? {};
    return Object.fromEntries(Object.entries(fields).map(([name, kind]) => [name, field((source as Record<string, unknown>)[name], kind)])) as T;
  } catch (error) { throw new RPCFault(-32602, `decode params: ${errorMessage(error)}`); }
}
const rawInit = new WeakMap<object,string>();
export function decodeRequest(line: string): Wire.RPCRequest | undefined {
  const request = decodeEnvelope(line);
  if(request?.method === 'plugin/init') rawInit.set(request,line);
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
  context: Context;
  private attempted = false;
  private initialized = false;
  constructor(plugin: ServerPlugin, context: Context, secrets: SecretTracker) { this.plugin = plugin; this.context = context; this.secrets = secrets; }
  private async identity(value: unknown): Promise<void> { if (value !== undefined) await this.plugin.identity?.(this.context, value); }
  async dispatch(req: Wire.RPCRequest): Promise<Wire.RPCResponse | undefined> {
    const id = req.id;
    try {
      const result = await this.call(req);
      return id === undefined ? undefined : { jsonrpc: '2.0', id, result };
    } catch (error) {
      if (id === undefined) return undefined;
      const fault = error instanceof InitError ? {code:-32602,message:error.message,data:error.rpcData()} : error instanceof RPCFault ? {code: error.code, message: error.message} : pluginError(error);
      return {jsonrpc: '2.0', id, error: fault};
    }
  }
  private async lifecycle<T>(call: () => T | Promise<T>): Promise<T> {
    try { return await call(); } catch (error) { throw new RPCFault(-32603, errorMessage(error)); }
  }
  private async call(req: Wire.RPCRequest): Promise<unknown> {
    const p = this.plugin;
    const ctx = this.context;
    if(req.method !== 'plugin/init' && !this.initialized) throw new RPCFault(-32600,'successful init required');
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
            const envelope = JSON.parse(raw) as Record<string,unknown>;
            const keys = Object.keys(envelope);
            const f = decodeJSONObject(raw,keys);
            if(req.jsonrpc !== '2.0') throw new RPCFault(-32600,'init requires JSON-RPC 2.0');
            input = decodeInitParams(f.get('params') ?? 'null');
          } else input = decodeInitParams(JSON.stringify(req.params));
        } catch(error) { if(error instanceof InitError || error instanceof RPCFault) throw error; throw new InitError('invalid_init','params'); }
        this.context = { ...ctx, config: new ConfigReader(input.config, this.secrets) };
        const authored = await this.lifecycle(() => p.init(this.context, input));
        const {reverse_rpc_version: _reverse, hooks_profile_version: _hooks, ...base} = authored;
        const result = decodeInitResult(encodeInitResult(base));
        validateInitResult(input,result);
        await this.identity(input.identity);
        this.initialized = true;
        return result;
      }
      case 'plugin/load': {
        const result = await this.lifecycle(() => p.load(ctx));
        return optionalObject(result, ['skipped_registrations']);
      }
      case 'plugin/unload': await this.lifecycle(() => p.unload(ctx)); return {ok: true};
      case 'plugin/health': {
        if (!p.health) return {ok: true};
        try { const result = await p.health(ctx); return {ok: result.ok ?? false, ...optionalObject(result, ['message'])}; }
        catch (error) { return {ok: false, message: errorMessage(error)}; }
      }
      case 'command/execute': {
        if (!p.command) return notImplemented('CommandHandler');
        const input = params<Wire.CommandExecParams>(req.params, {name:'string',session_id:'string',args:'string',identity:'json'});
        await this.identity(input.identity);
        const result = await p.command(ctx, input);
        return {action: result.action ?? '',...optionalObject(result, ['content']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'event/handle': {
        if (!p.eventHandle) return notImplemented('EventHandler');
        const input = params<Wire.EventHandleParams>(req.params, {type:'string',source:'string',data:'object',session_id:'string',pre_hook:'bool',identity:'json'});
        await this.identity(input.identity);
        const result = await p.eventHandle(ctx, input);
        return {...optionalObject(result, ['cancel','reason']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'crud/create': case 'crud/read': case 'crud/update': case 'crud/delete': case 'crud/list': {
        if (!p.create || !p.read || !p.update || !p.delete || !p.list) return notImplemented('CRUDHandler');
        const input = params<Wire.CRUDParams>(req.params, {resource_type:'string',id:'string',data:'object',filters:'object'});
        if (req.method === 'crud/create') return {data: await p.create(ctx,input.resource_type,input.data ?? null)};
        if (req.method === 'crud/read') return {data: await p.read(ctx,input.resource_type,input.id ?? '')};
        if (req.method === 'crud/update') return {data: await p.update(ctx,input.resource_type,input.id ?? '',input.data ?? null)};
        if (req.method === 'crud/delete') { await p.delete(ctx,input.resource_type,input.id ?? ''); return {ok:true}; }
        return {items: await p.list(ctx,input.resource_type,input.filters ?? null) ?? []};
      }
      case 'mcp/call_tool': {
        if (!p.mcpCallTool) return notImplemented('MCPHandler');
        const input = params<Wire.MCPCallRequest>(req.params, {tool_name:'string',arguments:'object',session_id:'string',identity:'json'});
        await this.identity(input.identity);
        const result = await p.mcpCallTool(ctx,input);
        return {content: result.content ?? null,...optionalObject(result, ['is_error']), ...(envelopes(result.envelopes) ? {envelopes: envelopes(result.envelopes)} : {})};
      }
      case 'http/handle': {
        if (!p.httpHandle) return notImplemented('HTTPHandler');
        const input = params<import('./types.js').HTTPRequest>(req.params, {method:'string',path:'string',raw_path:'string',raw_query:'string',query:'strings',headers:'strings',body:'base64',session_id:'string',identity:'json'});
        await this.identity(input.identity);
        const result = await p.httpHandle(ctx,input);
        return {status: result.status ?? 0, ...optionalObject(result, ['headers']), ...(result.body?.length ? {body: Buffer.from(result.body).toString('base64')} : {})};
      }
      case 'plugin/migrate': {
        if (!p.migrate) return notImplemented('Migrator');
        const input = params<Wire.MigrateParams>(req.params, {from_version:'string',to_version:'string',data_dir:'string'});
        await p.migrate(ctx,input.from_version,input.to_version); return {};
      }
      default: throw new RPCFault(-32601, `unknown method ${JSON.stringify(req.method)}`);
    }
  }
}
