import type { HostClient } from './host-client.js';
import type { ForwardContext } from './host-rpc.js';
import type * as Wire from './wire.js';
import type { ConfigReader } from './config.js';
import type { Logger } from './log.js';
export type Awaitable<T> = T | Promise<T>;
export interface Context {
  /** SDK-owned; absent in base and hooks-only production connections. */
  readonly host?: HostClient | undefined;
  readonly forwardContext?: ForwardContext | undefined;
  readonly signal: AbortSignal;
  readonly logger: Logger;
  readonly config: ConfigReader;
}
export interface Plugin {
  init(context: Context, params: Wire.InitParams): Awaitable<Wire.InitResult>;
  load(context: Context): Awaitable<Wire.LoadResult>;
  unload(context: Context): Awaitable<void>;
}
/** Opts into hooks/1 when offered during Init. Context supplies no host client. */
export interface HookHandler { hookHandle(context: Context, params: import('./hooks.js').HookRequest): Awaitable<import('./hooks.js').HookResult>; }
export interface CommandHandler { command(context: Context, params: Wire.CommandExecParams): Awaitable<Wire.CommandExecResult>; }
export interface EventHandler { eventHandle(context: Context, params: Wire.EventHandleParams): Awaitable<Wire.EventHandleResult>; }
export interface HealthChecker { health(context: Context): Awaitable<Wire.HealthResult>; }
export interface CRUDHandler {
  create(context: Context, resourceType: string, data: Record<string, unknown> | null): Awaitable<Record<string, unknown> | null>;
  read(context: Context, resourceType: string, id: string): Awaitable<Record<string, unknown> | null>;
  update(context: Context, resourceType: string, id: string, data: Record<string, unknown> | null): Awaitable<Record<string, unknown> | null>;
  delete(context: Context, resourceType: string, id: string): Awaitable<void>;
  list(context: Context, resourceType: string, filters: Record<string, unknown> | null): Awaitable<Array<Record<string, unknown>> | null>;
}
export interface MCPHandler { mcpCallTool(context: Context, params: Wire.MCPCallRequest): Awaitable<Wire.MCPCallResult>; }
export interface HTTPRequest extends Omit<Wire.HTTPRequest, 'body'> { body?: Uint8Array; }
export interface HTTPResponse extends Omit<Wire.HTTPResponse, 'body'> { body?: Uint8Array; }
export interface HTTPHandler { httpHandle(context: Context, params: HTTPRequest): Awaitable<HTTPResponse>; }
export interface Migrator { migrate(context: Context, fromVersion: string, toVersion: string): Awaitable<void>; }
export interface IdentityAware { identity(context: Context, value: unknown): Awaitable<void>; }
export type ServerPlugin = Plugin & Partial<HookHandler & CommandHandler & EventHandler & HealthChecker & CRUDHandler & MCPHandler & HTTPHandler & Migrator & IdentityAware>;
