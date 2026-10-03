import { inspectEnvelope, validateJSON } from './strict-json.js';
import type { RPCID, RPCRequest } from './wire.js';

export class EnvelopeFault extends Error {
  readonly code: number;
  readonly id: RPCID | null;
  constructor(code: number, id: RPCID | null, message: string) {
    super(message);
    this.code = code;
    this.id = id;
  }
}
/** Classify envelopes before dispatch. undefined is an unsolicited valid reply. */
export function decodeEnvelope(line: string): RPCRequest | undefined {
  try { JSON.parse(line); }
  catch { throw new EnvelopeFault(-32700,null,'parse error: invalid JSON'); }
  let fields: Map<string,string> | undefined;
  let duplicates: Set<string>;
  let invalidKeys: boolean;
  try { ({fields, duplicates, invalidKeys} = inspectEnvelope(line)); }
  catch { throw new EnvelopeFault(-32600,null,'invalid request'); }
  const invalid = (id: RPCID | null): never => { throw new EnvelopeFault(-32600,id,'invalid request'); };
  if (!fields) return invalid(null);
  const raw = fields.get('id');
  let id: RPCID | null = null;
  if (raw !== undefined && !duplicates.has('id')) {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed === 'string') {
      try { validateJSON(raw); } catch { return invalid(null); }
      id = parsed;
    }
    else if (parsed === null) id = null;
    else if (/^-?(?:0|[1-9][0-9]*)$/.test(raw) && typeof parsed === 'number' && Number.isSafeInteger(parsed)) id = parsed;
    else return invalid(null);
  }
  if (invalidKeys || duplicates.size || fields.get('jsonrpc') === undefined || JSON.parse(fields.get('jsonrpc')!) !== '2.0') return invalid(id);
  const method = fields.get('method');
  const hasResult = fields.has('result'), hasError = fields.has('error');
  if (method === undefined && raw !== undefined && hasResult !== hasError) {
    if (fields.has('params') || (hasResult && id === null)) return invalid(id);
    if (hasError) {
      const error: unknown = JSON.parse(fields.get('error')!);
      if (!error || typeof error !== 'object' || Array.isArray(error)) return invalid(id);
      const fault = error as Record<string,unknown>;
      if (!Number.isSafeInteger(fault.code) || typeof fault.message !== 'string') return invalid(id);
      try { validateJSON(JSON.stringify(fault.message)); } catch { return invalid(id); }
    }
    return undefined;
  }
  if (hasResult || hasError || method === undefined || (raw !== undefined && id === null)) return invalid(id);
  const name: unknown = JSON.parse(method);
  if (typeof name !== 'string') return invalid(id);
  try { validateJSON(method); } catch { return invalid(id); }
  return {jsonrpc:'2.0',method:name,...(raw === undefined ? {} : {id:id!}),...(fields.has('params') ? {params:JSON.parse(fields.get('params')!)} : {})};
}
