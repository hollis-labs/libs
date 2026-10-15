import { decodeForwardContext } from './host-rpc.js';
import { decodeJSONObject, validateJSON, validatePortableJSON, parseJSONTokens } from './strict-json.js';
import type { Grant, RuntimeIdentity, InitParams, InitResult, HostServices } from './wire.js';
export type InitFailureCode = 'invalid_init' | 'protocol_mismatch' | 'capability_contract_mismatch' | 'profile_mismatch';
export class InitError extends Error {
  readonly code: InitFailureCode; readonly field: string; readonly expected: number | undefined; readonly received: number | undefined;
  constructor(code: InitFailureCode, field: string, expected?: number, received?: number) { super(`subprocess init: ${code} (${field})`); this.code=code; this.field=field; this.expected=expected; this.received=received; }
  rpcData(): Record<string, unknown> { return {contract:'plugin-init/2', code:this.code, field:this.field, ...(this.expected === undefined ? {} : {expected:this.expected, received:this.received})}; }
}
function invalid(field: string): never { throw new InitError('invalid_init', field); }
function fields(raw: string, field: string, required: string[], optional: string[] = []): Map<string,string> {
  try { return decodeJSONObject(raw,required,optional); } catch { return invalid(field); }
}
function integer(raw: string, field: string, max = 4294967295): number {
  if (!/^(?:0|[1-9][0-9]*)$/.test(raw) || BigInt(raw) > BigInt(max)) invalid(field);
  return Number(raw);
}
function version(raw: string, field: string, code: InitFailureCode, expected: number): number {
  const received = integer(raw,field); if (received !== expected) throw new InitError(code,field,expected,received); return received;
}
function string(raw: string, field: string, nonblank = true): string {
  const value: unknown = parseJSONTokens(raw); if (typeof value !== 'string' || (nonblank && !value.trim())) invalid(field); return value;
}
function object(raw: string, field: string): Record<string, unknown> {
  const value: unknown = parseJSONTokens(raw); if (value === null || typeof value !== 'object' || Array.isArray(value)) invalid(field); return value as Record<string,unknown>;
}
function array(raw: string, field: string): unknown[] { const value: unknown = parseJSONTokens(raw); if (!Array.isArray(value)) invalid(field); return value; }
export function decodeRuntimeIdentity(raw: string): RuntimeIdentity {
  const f = fields(raw,'incarnation',['host_instance','owner_id','owner_generation']);
  const generation = integer(f.get('owner_generation')!,'owner_generation',Number.MAX_SAFE_INTEGER); if (!generation) invalid('owner_generation');
  return {host_instance:string(f.get('host_instance')!,'host_instance'), owner_id:string(f.get('owner_id')!,'owner_id'),owner_generation:generation};
}
export function grantTimestamp(value:string):bigint{return timestamp(JSON.stringify(value),'timestamp');}
function timestamp(raw: string, field: string): bigint {
  const text = string(raw,field); const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(text); if (!match) invalid(field);
  const [year,month,day,hour,minute,second] = match.slice(1,7).map(Number);
  const date = new Date(0); date.setUTCFullYear(year!,month!-1,day!); date.setUTCHours(hour!,minute!,second!,0);
  if (date.getUTCFullYear() !== year || date.getUTCMonth()+1 !== month || date.getUTCDate() !== day || date.getUTCHours() !== hour || date.getUTCMinutes() !== minute || date.getUTCSeconds() !== second) invalid(field);
  return BigInt(date.getTime())*1000000n + BigInt((match[7] ?? '').padEnd(9,'0'));
}
const rawScopes=new WeakMap<Grant,{raw:string,value:string}>();
export function grantScopeJSON(grant:Grant):string {
 const saved=rawScopes.get(grant),value=JSON.stringify(grant.scope);
 return saved?.value===value?saved.raw:value;
}
export function cloneGrantSet(grants:Grant[]):Grant[] {
 return grants.map(grant=>{ const copy=decodeGrant(encodeGrant(grant)); rawScopes.set(copy,{raw:grantScopeJSON(grant),value:JSON.stringify(copy.scope)});return copy; });
}
export function decodeGrant(raw: string): Grant {
  const names = ['grant_id','name','schema_version','scope','host_instance','owner_id','owner_generation','audience','issued_at','expires_at','policy_revision'];
  const f = fields(raw,'grant',names);
  for (const key of ['grant_id','name','host_instance','owner_id','audience','policy_revision']) string(f.get(key)!,key);
  if (!integer(f.get('schema_version')!,'schema_version')) invalid('schema_version');
  if (!integer(f.get('owner_generation')!,'owner_generation',Number.MAX_SAFE_INTEGER)) invalid('owner_generation');
  if (timestamp(f.get('expires_at')!,'expires_at') <= timestamp(f.get('issued_at')!,'issued_at')) invalid('expires_at');
  try { validatePortableJSON(f.get('scope')!); } catch { invalid('scope'); }
  const grant=parseJSONTokens(raw) as Grant;
 rawScopes.set(grant,{raw:f.get('scope')!,value:JSON.stringify(grant.scope)});
 return grant;
}
// Parse each array element from its original tokens before JSON.parse can round it.
function rawArray(raw: string, field: string): string[] {
  array(raw,field);
  const result: string[] = []; let start=1, depth=0, quoted=false, escaped=false;
  for(let i=1;i<raw.length-1;i++) { const c=raw[i]; if(quoted) { if(escaped) escaped=false; else if(c==='\\') escaped=true; else if(c==='"') quoted=false; } else if(c==='"') quoted=true; else if(c==='{' || c==='[') depth++; else if(c==='}' || c===']') depth--; else if(c===',' && depth===0) {result.push(raw.slice(start,i).trim()); start=i+1;} }
  const last=raw.slice(start,-1).trim(); if(last) result.push(last); return result;
}
export function decodeGrantSet(raw: string, incarnation?: RuntimeIdentity): Grant[] {
  try {validateJSON(raw);} catch {invalid('grants');}
  const grants=rawArray(raw.trim(),'grants').map(decodeGrant); const ids=new Set<string>();
  for(const g of grants) {if(ids.has(g.grant_id)) invalid('grant_id'); ids.add(g.grant_id); if(incarnation && (g.host_instance!==incarnation.host_instance || g.owner_id!==incarnation.owner_id || g.owner_generation!==incarnation.owner_generation)) invalid('grants/incarnation');} return grants;
}
const methods = new Set(['host/storage/get','host/storage/put','host/storage/delete','host/secrets/get','host/egress/request','host/events/publish','host/log','host/readonly/query','host/mcp/list_tools','host/mcp/call_tool','host/mcp/cancel_call','host/bindings/renew']);
function hostServices(raw: string, incarnation: RuntimeIdentity): HostServices {
  const f=fields(raw,'host_services',['reverse_rpc_version','incarnation','methods','limits']); version(f.get('reverse_rpc_version')!,'host_services.reverse_rpc_version','profile_mismatch',1);
  const tuple=decodeRuntimeIdentity(f.get('incarnation')!); if(tuple.host_instance!==incarnation.host_instance || tuple.owner_id!==incarnation.owner_id || tuple.owner_generation!==incarnation.owner_generation) invalid('host_services.incarnation');
  const names=['host_to_plugin_inflight','plugin_to_host_inflight','host_global_inflight','control_slots','max_frame_bytes','max_queued_write_bytes','write_timeout_ms','max_depth','method_timeout_ms'];
  const limits=fields(f.get('limits')!,'host_services.limits',names); const nums: Record<string,number>={};
  for(const key of names.filter(k=>k!=='method_timeout_ms')) {nums[key]=integer(limits.get(key)!,key); if(!nums[key]) invalid('host_services.limits');}
  if(nums.control_slots!<2 || nums.max_queued_write_bytes!<nums.max_frame_bytes!) invalid('host_services.limits');
  const offered=array(f.get('methods')!,'host_services.methods'); const timeouts=object(limits.get('method_timeout_ms')!,'method_timeout_ms');
  const timeoutFields=fields(limits.get('method_timeout_ms')!,'method_timeout_ms',Object.keys(timeouts));
  if(Object.keys(timeouts).length!==offered.length || new Set(offered).size!==offered.length) invalid('host_services.methods');
  for(const m of offered) if(typeof m!=='string' || !methods.has(m) || !timeoutFields.has(m) || !integer(timeoutFields.get(m)!,'method_timeout_ms')) invalid('host_services.methods');
  return parseJSONTokens(raw) as HostServices;
}
export function decodeInitParams(raw: string): InitParams {
  const f=fields(raw,'params',['plugin_dir','data_dir','cache_dir','config','log_level','host_info','capability_contract','incarnation','grants'],['identity','host_services','hooks_profile','context','grants_renewal_version']);
  if(f.has('context')) {try { decodeForwardContext(f.get('context')!); } catch { invalid('context'); }}
  const host=fields(f.get('host_info')!,'host_info',['version','protocol']); version(host.get('protocol')!,'host_info.protocol','protocol_mismatch',2); string(host.get('version')!,'host_info.version');
  version(f.get('capability_contract')!,'capability_contract','capability_contract_mismatch',1);
  for(const key of ['plugin_dir','data_dir','cache_dir']) string(f.get(key)!,key);
  if(!['debug','info','warn','error'].includes(string(f.get('log_level')!,'log_level'))) invalid('log_level');
  if(Object.values(object(f.get('config')!,'config')).some(v=>typeof v!=='string')) invalid('config');
  const incarnation=decodeRuntimeIdentity(f.get('incarnation')!); decodeGrantSet(f.get('grants')!,incarnation);
  if(f.has('host_services')) hostServices(f.get('host_services')!,incarnation);
  if(f.has('grants_renewal_version'))version(f.get('grants_renewal_version')!,'grants_renewal_version','profile_mismatch',1);
 if(f.has('hooks_profile')) {const h=fields(f.get('hooks_profile')!,'hooks_profile',['hooks_profile_version']); version(h.get('hooks_profile_version')!,'hooks_profile.hooks_profile_version','profile_mismatch',1);}
  const params=parseJSONTokens(raw) as InitParams;params.grants=decodeGrantSet(f.get('grants')!,incarnation);return params;
}
export function decodeInitResult(raw: string): InitResult {
  const f=fields(raw,'result',['id','name','version','description','protocol','capability_contract'],['reverse_rpc_version','hooks_profile_version','grants_renewal_version']);
  for(const key of ['id','name','version']) string(f.get(key)!,key); string(f.get('description')!,'description',false);
  version(f.get('protocol')!,'protocol','protocol_mismatch',2); version(f.get('capability_contract')!,'capability_contract','capability_contract_mismatch',1);
  for(const key of ['reverse_rpc_version','hooks_profile_version','grants_renewal_version']) if(f.has(key)) version(f.get(key)!,key,'profile_mismatch',1);
  return parseJSONTokens(raw) as InitResult;
}
export function validateInitResult(input: InitParams, result: InitResult): void {
  encodeInitParams(input); encodeInitResult(result);
  if(result.grants_renewal_version!==undefined && input.grants_renewal_version===undefined)throw new InitError('profile_mismatch','grants_renewal_version',0,result.grants_renewal_version);
 if(result.reverse_rpc_version!==undefined && !input.host_services) throw new InitError('profile_mismatch','reverse_rpc_version',0,result.reverse_rpc_version);
  if(result.hooks_profile_version!==undefined && !input.hooks_profile) throw new InitError('profile_mismatch','hooks_profile_version',0,result.hooks_profile_version);
}

// Checked producer encoding prevents JSON.stringify from replacing nonfinite
// numbers or silently losing non-JSON values inside opaque payloads.
function encode(value: unknown, field: string): string {
  try {
    return JSON.stringify(value, function(this: unknown, key, item: unknown) {
      if (typeof item === 'number' && !Number.isFinite(item) || typeof item === 'function' || typeof item === 'symbol' || typeof item === 'bigint') invalid(field);
      const optional = field === 'params' ? ['identity','host_services','hooks_profile','context','grants_renewal_version'] : field === 'result' ? ['reverse_rpc_version','hooks_profile_version','grants_renewal_version'] : [];
      if (item === undefined && !(this === value && optional.includes(key))) invalid(field);
      return item;
    });
  } catch (error) { if(error instanceof InitError) throw error; return invalid(field); }
}
export function encodeInitParams(value: InitParams): string { const raw=encode(value,'params'); decodeInitParams(raw); return raw; }
export function encodeInitResult(value: InitResult): string { const raw=encode(value,'result'); decodeInitResult(raw); return raw; }
export function encodeGrant(value: Grant): string { const raw=encode(value,'grant'); decodeGrant(raw); return raw; }
