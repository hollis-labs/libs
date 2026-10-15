import { Buffer } from 'node:buffer';
import { decodeJSONObject, rawJSONItems, parseJSONTokens } from './strict-json.js';
import { decodeForwardContext, validateTimestamp } from './host-rpc.js';
import { decodeRuntimeIdentity } from './init-contract.js';
import { authoredResult } from './payload.js';
import { rawJSON } from './raw-json.js';
import type { RawJSON } from './raw-json.js';
import type * as Wire from './hooks-wire.js';
import type { RPCError } from './wire.js';
export * from './hooks-wire.js';
export { rawJSON } from './raw-json.js';
export type { RawJSON } from './raw-json.js';
export const HOOKS_PROFILE_VERSION = 1;
export const MAX_HOOK_BATCH_ITEMS = 64;
export const MAX_HOOK_DTO_BYTES = 1 << 20;
export interface HookRequest extends Wire.HookHandleParams { readonly payloadJSON: RawJSON; }
/** Results choose either parsed payload or payloadJSON, never both. */
export type HookResult = Omit<Wire.HookHandleResult, 'payload'> & {payload?: unknown; payloadJSON?: RawJSON};
export class HookValidationError extends Error {
  readonly field: string;
  readonly reason: string;
  constructor(field: string, reason: string) { super('hooks: ' + field + ': ' + reason); this.name = 'HookValidationError'; this.field=field;this.reason=reason; }
}
const invalid = (field: string, reason = 'invalid field'): never => { throw new HookValidationError(field, reason); };
const failureCodes: readonly string[] = ['remote_not_allowed','latency_budget_exceeded','schema_mismatch','profile_unavailable','stale_scope','stale_binding','capacity_exhausted','deadline_exceeded','caller_cancelled','depth_exceeded','callback_cycle','transport_failure','handler_panic','invalid_output','handler_error'];
const paramFields = ['invocation_id','catalog_version','hook','schema_digest','kind','mode','scope','context','payload','metadata','deadline','aggregate_budget_ms','depth','trace','root_invocation_id'];
function fields(raw: string, required: string[], optional: string[] = [], nullable: string[] = []): Map<string,string> {
  if (Buffer.byteLength(raw) > MAX_HOOK_DTO_BYTES) invalid('dto','oversized JSON');
  try { rawJSON(raw); return decodeJSONObject(raw,required,optional,nullable); } catch (err) { if(err instanceof HookValidationError) throw err; return invalid('dto','invalid or ambiguous JSON'); }
}
function text(raw: string, field: string, max = 256, blank = false): string {
  const v = parseJSONTokens(raw); if(typeof v !== 'string' || [...v].length > max || !blank && !v.trim()) invalid(field);
  return v as string;
}
function enumValue(raw: string, field: string, values: readonly string[]): string {
  const v = text(raw,field); if(!values.includes(v)) invalid(field,'unknown enum'); return v;
}
function u32(raw: string,field: string): number {
  if(!/^[0-9]+$/.test(raw) || BigInt(raw)<1n || BigInt(raw)>4294967295n) invalid(field,'invalid integer token');return Number(raw);
}
export function decodeHookTrace(raw: string): Wire.HookTrace {
  const f = fields(raw,['trace_id','span_id'],['parent_span_id']);
  for(const [key,v] of f) { const s = text(v,key); const n = key === 'trace_id' ? 32 : 16; if(s.length !== n || !/^[0-9a-f]+$/.test(s) || /^0+$/.test(s)) invalid(key,'invalid trace identifier'); }
  return parseJSONTokens(raw) as Wire.HookTrace;
}
export function decodeHookScope(raw: string): Wire.HookScope {
  const f=fields(raw,['incarnation','registration_id']);
  try { decodeRuntimeIdentity(f.get('incarnation')!); } catch { invalid('scope.incarnation'); }
  text(f.get('registration_id')!,'registration_id');return parseJSONTokens(raw) as Wire.HookScope;
}
export function decodeHookFailure(raw: string): Wire.HookFailure {
  const f=fields(raw,['code'],['message']);enumValue(f.get('code')!,'code',failureCodes);
  if(f.has('message')) text(f.get('message')!,'message',16384,true);
  return parseJSONTokens(raw) as Wire.HookFailure;
}
export function decodeHookErrorData(raw: string): Wire.HookErrorData {
  const f=fields(raw,['contract','code'],['field']);enumValue(f.get('contract')!,'contract',['hooks/1']);enumValue(f.get('code')!,'code',['invalid_params','profile_unavailable','invalid_request','parse_error','method_not_found']);
  if(f.has('field')) text(f.get('field')!,'field');return parseJSONTokens(raw) as Wire.HookErrorData;
}
export function encodeHookErrorData(v: Wire.HookErrorData): string {const raw=JSON.stringify(authoredResult('hooks',v,MAX_HOOK_DTO_BYTES));decodeHookErrorData(raw);return raw;}
/** Structural mapping only; never interprets -32003 or -32010 as a veto. */
export function hookRPCError(code: number, cause: Wire.HookErrorData['code'], field?: string): RPCError {
  if(![-32700,-32600,-32601,-32602].includes(code)) invalid('code','unsupported hooks RPC error code');
  if(!(code===-32700&&cause==='parse_error'||code===-32600&&cause==='invalid_request'||code===-32602&&cause==='invalid_params'||code===-32601&&['profile_unavailable','method_not_found'].includes(cause))) invalid('code','RPC code/cause mismatch');
  const data: Wire.HookErrorData={contract:'hooks/1',code:cause,...(field===undefined?{}:{field})};encodeHookErrorData(data);
  return {code,message:'hook request rejected',data};
}
export function decodeHookHandleParams(raw: string): HookRequest {
  const f=fields(raw,paramFields,['parent_invocation_id'],['payload']);
  for(const key of ['invocation_id','catalog_version','schema_digest','root_invocation_id','parent_invocation_id']) if(f.has(key)) text(f.get(key)!,key);
  const name=text(f.get('hook')!,'hook');if(!/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/.test(name)) invalid('hook');
  const kind=enumValue(f.get('kind')!,'kind',['action','filter']);const mode=enumValue(f.get('mode')!,'mode',['sequential','parallel','bail','waterfall','async','after_commit']);
  if((kind==='filter') !== (mode==='waterfall')) invalid('mode','kind/mode mismatch');
  decodeHookScope(f.get('scope')!);decodeHookTrace(f.get('trace')!);
  try { decodeForwardContext(f.get('context')!); } catch { invalid('context'); }
  try { validateTimestamp(parseJSONTokens(f.get('deadline')!), 'deadline'); } catch { invalid('deadline'); }
  u32(f.get('aggregate_budget_ms')!,'aggregate_budget_ms');u32(f.get('depth')!,'depth');
  const metadata=fields(f.get('metadata')!,[],rawJSONItems(f.get('metadata')!).map(member => {
    const m=/^("(?:[^"\\]|\\.)*")\s*:/.exec(member)!;return parseJSONTokens(m[1]!) as string;
  }));
  for(const [key,v] of metadata) text(v,'metadata.'+key,Number.MAX_SAFE_INTEGER,true);
  return {...parseJSONTokens(raw) as Wire.HookHandleParams,payloadJSON:rawJSON(f.get('payload')!)};
}
const resultPayloads = new WeakMap<object, RawJSON>();
export function hookPayloadJSON(result: Wire.HookHandleResult): RawJSON | undefined { return resultPayloads.get(result); }
export function decodeHookHandleResult(raw: string): Wire.HookHandleResult {
  const f=fields(raw,['invocation_id','status'],['payload','reason','error'],['payload']);
  text(f.get('invocation_id')!,'invocation_id');const status=enumValue(f.get('status')!,'status',['ok','cancelled','approval_required','failed','unavailable']);
  if(f.has('reason')) text(f.get('reason')!,'reason',16384,true);if(f.has('error')) decodeHookFailure(f.get('error')!);
  if(status==='ok' && (f.has('reason') || f.has('error')) || (status==='cancelled'||status==='approval_required') && (f.has('payload')||f.has('error')) || (status==='failed'||status==='unavailable') && (f.has('payload')||f.has('reason')||!f.has('error'))) invalid('status','contradictory result branch');
  const result = parseJSONTokens(raw) as Wire.HookHandleResult;
  if(f.has('payload')) resultPayloads.set(result,rawJSON(f.get('payload')!));
  freezeResult(result);
  return result;
}
function freezeResult(value: unknown): void {
  if(value && typeof value==='object') {for(const v of Object.values(value)) freezeResult(v);Object.freeze(value);}
}
function encode(value: object, result: boolean): string {
  if(!value || (Object.getPrototypeOf(value)!==Object.prototype && Object.getPrototypeOf(value)!==null) || Object.getOwnPropertySymbols(value).length) invalid('dto','non-JSON object');
  const descriptors=Object.getOwnPropertyDescriptors(value);
  for(const d of Object.values(descriptors)) if(!d.enumerable||!Object.hasOwn(d,'value')) invalid('dto','non-JSON property');
  const rawView=descriptors.payloadJSON?.value as unknown;
  if(rawView!==undefined && descriptors.payload?.value!==undefined && result) invalid('payload','payload and payloadJSON are mutually exclusive');
  const literal=rawView!==undefined?rawJSON(rawView as string):result?resultPayloads.get(value):undefined;
  const source: Record<string,unknown>={};
  for(const [key,d] of Object.entries(descriptors)) if(key!=='payloadJSON' && (key!=='payload'||literal===undefined)) Object.defineProperty(source,key,{value:d.value,enumerable:true});
  // RawJSON is validated/spliced rather than quoted during preflight, so a
  // valid near-limit payload is not charged twice for its author view.
  const v=authoredResult('hooks',source,MAX_HOOK_DTO_BYTES);
  const entries=Object.entries(v).map(([k,x])=>JSON.stringify(k)+':'+JSON.stringify(x));
  if(literal!==undefined) entries.push('"payload":'+literal);
  const raw='{'+entries.join(',')+'}';
  if(result) decodeHookHandleResult(raw);else decodeHookHandleParams(raw);return raw;
}
export function encodeHookHandleParams(v: Wire.HookHandleParams | HookRequest): string {return encode(v,false);}
export function encodeHookHandleResult(v: Wire.HookHandleResult | HookResult): string {return encode(v,true);}
function batchItems(raw: string): string[] {
  const f=fields(raw,['items']);const value=f.get('items')!;if(!value.startsWith('[')) invalid('items');
  const items=rawJSONItems(value);if(items.length<1||items.length>MAX_HOOK_BATCH_ITEMS) invalid('items','batch requires 1..64 items');return items;
}
function unique(items: Array<{invocation_id: string}>): void {const seen=new Set<string>();for(const p of items){if(seen.has(p.invocation_id)) invalid('items','duplicate invocation_id');seen.add(p.invocation_id);}}
export function decodeHookHandleBatchParams(raw: string): {items: HookRequest[]} {const items=batchItems(raw).map(decodeHookHandleParams);unique(items);return {items};}
export function decodeHookHandleBatchResult(raw: string): Wire.HookHandleBatchResult {const items=batchItems(raw).map(decodeHookHandleResult);unique(items);return {items};}
function validateBatchSource(v: object): void {
  if(!v || (Object.getPrototypeOf(v)!==Object.prototype && Object.getPrototypeOf(v)!==null) || Object.getOwnPropertySymbols(v).length) invalid('items','required closed batch object');
  const props=Object.getOwnPropertyDescriptors(v),d=props.items;
  if(Object.keys(props).length!==1||!d||!d.enumerable||!Object.hasOwn(d,'value')||!Array.isArray(d.value)) invalid('items','required closed batch object');
  const items=d.value as unknown[];
  if(items.length<1||items.length>MAX_HOOK_BATCH_ITEMS||Object.getOwnPropertySymbols(items).length) invalid('items','batch requires 1..64 items');
  const entries=Object.getOwnPropertyDescriptors(items);
  if(Object.keys(entries).length!==items.length+1) invalid('items','sparse or extended array');
  for(let i=0;i<items.length;i++) {const item=entries[String(i)];if(!item||!item.enumerable||!Object.hasOwn(item,'value')) invalid('items','non-JSON array item');}
}
export function encodeHookHandleBatchParams(v: {items: Array<Wire.HookHandleParams | HookRequest>}): string {validateBatchSource(v);const raw='{"items":['+v.items.map(encodeHookHandleParams).join(',')+']}';decodeHookHandleBatchParams(raw);return raw;}
export function encodeHookHandleBatchResult(v: {items: Array<Wire.HookHandleResult | HookResult>}): string {validateBatchSource(v);const raw='{"items":['+v.items.map(encodeHookHandleResult).join(',')+']}';decodeHookHandleBatchResult(raw);return raw;}
export function validateHookRequest(p: Wire.HookHandleParams | HookRequest, notification: boolean): void {
  decodeHookHandleParams(encodeHookHandleParams(p));
  if(notification) {if(p.kind!=='action'||!['async','after_commit'].includes(p.mode)) invalid('mode','illegal notification');}
  else if(p.context.binding_id===undefined) invalid('context.binding_id','required for request');
}
export function validateHookBatchRequest(p: {items: Array<Wire.HookHandleParams | HookRequest>}, notification: boolean): void {
  decodeHookHandleBatchParams(encodeHookHandleBatchParams(p));for(const item of p.items){validateHookRequest(item,notification);if(item.kind!=='action'||item.mode==='bail') invalid('items','batch requires observation actions');}
}
export function validateHookResultFor(p: Wire.HookHandleParams, r: Wire.HookHandleResult | HookResult): void {
  decodeHookHandleParams(encodeHookHandleParams(p));const result=decodeHookHandleResult(encodeHookHandleResult(r));
  if(result.invocation_id!==p.invocation_id) invalid('invocation_id','result correlation mismatch');
  if(result.status==='ok' && (p.kind==='filter')!==Object.hasOwn(result,'payload')) invalid('payload','kind/output mismatch');
  if(['cancelled','approval_required'].includes(result.status) && (p.kind!=='action'||p.mode!=='bail')) invalid('status','veto requires bail action');
}
export function validateHookBatchResultFor(p: Wire.HookHandleBatchParams,r: Wire.HookHandleBatchResult): void {
  decodeHookHandleBatchParams(encodeHookHandleBatchParams(p));decodeHookHandleBatchResult(encodeHookHandleBatchResult(r));if(p.items.length!==r.items.length) invalid('items','result count mismatch');
  p.items.forEach((item,i)=>validateHookResultFor(item,r.items[i]!));
}
