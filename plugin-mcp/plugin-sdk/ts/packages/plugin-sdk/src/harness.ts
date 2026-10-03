import { mkdtemp, mkdir, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import process from 'node:process';
import { Buffer } from 'node:buffer';
import { ConfigReader } from './config.js';
import { createLogger, SecretTracker } from './log.js';
import type { Context, ServerPlugin, HTTPRequest, HTTPResponse, Awaitable } from './types.js';
import type * as Wire from './wire.js';

export interface HarnessOptions {
  pluginDir?: string;
  config?: Record<string, string>;
  hostInfo?: Wire.HostInfo;
  granted?: string[];
  identity?: unknown;
  jsonRoundtrip?: boolean;
  writeLog?: (line: string) => void;
}
function clone<T>(value: T): T {
  // Reject values JSON.stringify normally drops silently (Go marshal fails on
  // functions/channels). Cycles and BigInt already fail in JSON.stringify.
  return JSON.parse(JSON.stringify(value, (_key, v: unknown) => {
    if (typeof v === 'function' || typeof v === 'symbol') throw new Error('unserializable JSON value');
    return v;
  })) as T;
}
/** Direct author API harness. close() removes only its own temp tree; unload() is explicit. */
export class Harness {
  readonly plugin: ServerPlugin;
  readonly pluginDir: string;
  readonly dataDir: string;
  readonly cacheDir: string;
  readonly roundtripEnabled: boolean;
  readonly context: Context;
  private readonly options: HarnessOptions;
  private owned: boolean;
  private constructor(plugin: ServerPlugin, pluginDir: string, owned: boolean, options: HarnessOptions) {
    this.plugin = plugin; this.pluginDir = pluginDir; this.owned = owned; this.options = options;
    this.dataDir = join(pluginDir,'data'); this.cacheDir = join(pluginDir,'cache');
    const environment = process.env.PLUGIN_SDK_JSON_ROUNDTRIP ?? '';
    this.roundtripEnabled = options.jsonRoundtrip ?? (environment !== '' && environment !== '0' && environment.toLowerCase() !== 'false');
    const secrets = new SecretTracker();
    this.context = {signal:new AbortController().signal,config:new ConfigReader(options.config ?? {},secrets),logger:createLogger({secrets,write:options.writeLog ?? (() => {})})};
  }
  static async create(plugin: ServerPlugin, options: HarnessOptions = {}): Promise<Harness> {
    const owned = options.pluginDir === undefined;
    const dir = options.pluginDir ?? await mkdtemp(join(tmpdir(),'plugin-sdk-harness-'));
    try {
      if (owned) { await mkdir(join(dir,'data')); await mkdir(join(dir,'cache')); }
      return new Harness(plugin,dir,owned,options);
    } catch (error) { if (owned) await rm(dir,{recursive:true,force:true}); throw error; }
  }
  private roundtrip<T>(value: T): T { return this.roundtripEnabled ? clone(value) : value; }
  private async invoke<P,R>(input: P, call: (params: P) => Awaitable<R>): Promise<R> { return this.roundtrip(await call(this.roundtrip(input))); }
  private missing(name: string): never { throw new Error(`plugin does not implement ${name}`); }
  async init(): Promise<Wire.InitResult> {
    return this.invoke({plugin_dir:this.pluginDir,data_dir:this.dataDir,cache_dir:this.cacheDir,config:this.options.config ?? {},log_level:'info',host_info:this.options.hostInfo ?? {version:'test',protocol:1},granted:this.options.granted,identity:this.options.identity}, input => this.plugin.init(this.context,input));
  }
  async load(): Promise<Wire.LoadResult> { return this.roundtrip(await this.plugin.load(this.context)); }
  async unload(): Promise<void> { await this.plugin.unload(this.context); }
  async command(name: string, sessionId = '', args = '', identity?: unknown): Promise<Wire.CommandExecResult> {
    if (!this.plugin.command) return this.missing('CommandHandler');
    return this.invoke({name,session_id:sessionId,args,identity}, input => this.plugin.command!(this.context,input));
  }
  async event(input: Wire.EventHandleParams): Promise<Wire.EventHandleResult> {
    if (!this.plugin.eventHandle) return this.missing('EventHandler');
    return this.invoke(input, params => this.plugin.eventHandle!(this.context,params));
  }
  async health(): Promise<Wire.HealthResult> { return this.plugin.health ? this.roundtrip(await this.plugin.health(this.context)) : {ok:true}; }
  async mcp(input: Wire.MCPCallRequest): Promise<Wire.MCPCallResult> {
    if (!this.plugin.mcpCallTool) return this.missing('MCPHandler');
    return this.invoke(input, params => this.plugin.mcpCallTool!(this.context,params));
  }
  async http(input: HTTPRequest): Promise<HTTPResponse> {
    if (!this.plugin.httpHandle) return this.missing('HTTPHandler');
    const params = this.roundtripEnabled ? clone({...input,body:input.body ? Buffer.from(input.body).toString('base64') : undefined}) : input;
    const decoded = this.roundtripEnabled ? {...params,body:typeof params.body === 'string' ? Buffer.from(params.body,'base64') : undefined} : input;
    const result = await this.plugin.httpHandle(this.context,decoded);
    if (!this.roundtripEnabled) return result;
    const output = clone({...result,body:result.body ? Buffer.from(result.body).toString('base64') : undefined});
    return {...output,body:output.body ? Buffer.from(output.body,'base64') : undefined};
  }
  async migrate(from: string, to: string): Promise<void> {
    if (!this.plugin.migrate) return this.missing('Migrator');
    const params = this.roundtrip({from,to}); await this.plugin.migrate(this.context,params.from,params.to);
  }
  async create(resourceType: string, data: Record<string,unknown>): Promise<Record<string,unknown> | null> {
    if (!this.plugin.create) return this.missing('CRUDHandler');
    return this.invoke({resourceType,data}, p => this.plugin.create!(this.context,p.resourceType,p.data));
  }
  async read(resourceType: string, id: string): Promise<Record<string,unknown> | null> {
    if (!this.plugin.read) return this.missing('CRUDHandler');
    return this.invoke({resourceType,id}, p => this.plugin.read!(this.context,p.resourceType,p.id));
  }
  async update(resourceType: string, id: string, data: Record<string,unknown>): Promise<Record<string,unknown> | null> {
    if (!this.plugin.update) return this.missing('CRUDHandler');
    return this.invoke({resourceType,id,data}, p => this.plugin.update!(this.context,p.resourceType,p.id,p.data));
  }
  async delete(resourceType: string, id: string): Promise<void> {
    if (!this.plugin.delete) return this.missing('CRUDHandler');
    const p = this.roundtrip({resourceType,id}); await this.plugin.delete(this.context,p.resourceType,p.id);
  }
  async list(resourceType: string, filters: Record<string,unknown> = {}): Promise<Array<Record<string,unknown>> | null> {
    if (!this.plugin.list) return this.missing('CRUDHandler');
    return this.invoke({resourceType,filters}, p => this.plugin.list!(this.context,p.resourceType,p.filters));
  }
  async close(): Promise<void> { if (this.owned) { await rm(this.pluginDir,{recursive:true,force:true}); this.owned = false; } }
}
export async function createHarness(plugin: ServerPlugin, options: HarnessOptions = {}): Promise<Harness> {
  return Harness.create(plugin,options);
}
