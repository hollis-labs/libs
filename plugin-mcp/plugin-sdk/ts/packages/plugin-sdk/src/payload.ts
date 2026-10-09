import { encodeBoundedJSON, DEFAULT_FRAME_BYTES } from './frame-codec.js';
import { Buffer } from 'node:buffer';
import { decodeJSONObject, inspectEnvelope, parseJSONTokens, validateJSON } from './strict-json.js';
import { decodeForwardContext } from './host-rpc.js';
import type { RPCRequest } from './wire.js';
export class PayloadError extends Error {
}
type Rule = (value: unknown) => void;
const fail = (): never => { throw new PayloadError('decode params: invalid field or required non-null object'); };
const record = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);
const string = (nonblank = false): Rule => v => { if (typeof v !== 'string' || nonblank && !v.trim())
    fail(); };
const object: Rule = v => { if (!record(v))
    fail(); };
const boolean: Rule = v => { if (typeof v !== 'boolean')
    fail(); };
const strings: Rule = v => { object(v); for (const x of Object.values(v as object))
    string()(x); };
const base64: Rule = v => { string()(v); const s = v as string; if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(s) || Buffer.from(s, 'base64').toString('base64') !== s)
    fail(); };
const rawParams = new WeakMap<object, string | undefined>();
export function rememberParams(request: RPCRequest, line: string): void { rawParams.set(request, inspectEnvelope(line).fields?.get('params')); }
export function requestParamsJSON(request: RPCRequest): string | undefined { return rawParams.has(request) ? rawParams.get(request) : request.params === undefined ? undefined : JSON.stringify(request.params); }
export function decodeRuntimeParams<T>(request: RPCRequest): T {
    const rules: Record<string, Rule> = { context: () => { } };
    let required: string[] = [];
    const add = (rule: Rule, ...names: string[]) => { for (const n of names)
        rules[n] = rule; };
    const name = request.method;
    switch (name) {
        case 'plugin/load':
        case 'plugin/unload':
        case 'plugin/health': break;
        case 'command/execute':
            required = ['name', 'session_id', 'args'];
            add(string(true), 'name');
            add(string(), 'session_id', 'args');
            add(() => { }, 'identity');
            break;
        case 'event/handle':
            required = ['type', 'source', 'data', 'pre_hook'];
            add(string(true), 'type', 'source');
            add(string(), 'session_id');
            add(object, 'data');
            add(boolean, 'pre_hook');
            add(() => { }, 'identity');
            break;
        case 'crud/create':
        case 'crud/read':
        case 'crud/update':
        case 'crud/delete':
        case 'crud/list':
            required = ['resource_type'];
            add(string(true), 'resource_type', 'id');
            add(object, 'data', 'filters');
            if (['crud/read', 'crud/update', 'crud/delete'].includes(name))
                required.push('id');
            if (['crud/create', 'crud/update'].includes(name))
                required.push('data');
            break;
        case 'mcp/call_tool':
            required = ['tool_name', 'arguments'];
            add(string(true), 'tool_name');
            add(object, 'arguments');
            add(string(), 'session_id');
            add(() => { }, 'identity');
            break;
        case 'http/handle':
            required = ['method', 'path'];
            add(string(true), 'method', 'path');
            add(string(), 'raw_path', 'raw_query', 'session_id');
            add(strings, 'headers', 'query');
            add(base64, 'body');
            add(() => { }, 'identity');
            break;
        case 'plugin/grants/renew':
 required=['renewal_version','sequence','incarnation','grants','context'];
 add(()=>{},'renewal_version','sequence','incarnation','grants');
 break;
 case 'plugin/migrate':
            required = ['from_version', 'to_version', 'data_dir'];
            add(string(true), ...required);
            break;
        default: fail();
    }
    try {
        const raw = requestParamsJSON(request);
        if (raw === undefined && !required.length)
            return {} as T;
        if (raw === undefined)
            fail();
        const fields = decodeJSONObject(raw!, required, Object.keys(rules).filter(k => !required.includes(k)));
        const result = parseJSONTokens(raw!) as Record<string, unknown>;
        inspectAuthored(result);
        for (const [key, value] of fields) {
            if (key === 'context')
                result.context = decodeForwardContext(value);
            else
                rules[key]!(result[key]);
        }
        if (['event/handle','mcp/call_tool','http/handle'].includes(name) && !fields.has('session_id')) result.session_id = '';
        if(name==='http/handle') for(const key of ['raw_path','raw_query']) if(!fields.has(key)) result[key]='';
        if (name === 'crud/list' && !fields.has('filters'))
            result.filters = {};
        if (name === 'http/handle' && fields.has('body'))
            result.body = Buffer.from(result.body as string, 'base64');
        return result as T;
    }
    catch {
        return fail();
    }
}
// Representability only; frame budgets belong to the framing slice.
function inspectAuthored(value: unknown, active = new Set<object>(), depth = 0): void {
    if (depth > 128)
        throw new Error('result nesting exceeds limit');
    if (value === null || typeof value === 'string' || typeof value === 'boolean')
        return;
    if (typeof value === 'number' && Number.isFinite(value))
        return;
    if (typeof value !== 'object')
        throw new Error('unserializable result value');
    if (active.has(value))
        throw new Error('cyclic result');
    if (!Array.isArray(value) && Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null)
        throw new Error('non-JSON result object');
    if (Object.getOwnPropertySymbols(value).length)
        throw new Error('symbol result property');
    active.add(value);
    const descriptors = Object.getOwnPropertyDescriptors(value);
    for (const [key, d] of Object.entries(descriptors)) {
        if (Array.isArray(value) && key === 'length')
            continue;
        if (!d.enumerable || !Object.hasOwn(d, 'value'))
            throw new Error('non-JSON result property');
        if (Array.isArray(value) && (!/^(?:0|[1-9][0-9]*)$/.test(key) || Number(key) >= value.length))
            throw new Error('extended result array');
        inspectAuthored(d.value, active, depth + 1);
    }
    if (Array.isArray(value) && Object.keys(descriptors).length !== value.length + 1)
        throw new Error('sparse result array');
    active.delete(value);
}
const array = (rule: Rule): Rule => v => { if (!Array.isArray(v))
    fail(); for (const item of v as unknown[])
    rule(item); };
const closed = (rules: Record<string, Rule>, required: string[], nullable: string[] = []): Rule => v => { object(v); const f = v as Record<string, unknown>; if (required.some(k => !Object.hasOwn(f, k)))
    fail(); for (const [k, x] of Object.entries(f)) {
    if (!Object.hasOwn(rules, k))
        fail();
    if (x === null && nullable.includes(k))
        continue;
    rules[k]!(x);
} };
const envelopes = array(closed({ type: string(true), data: object, session_id: string() }, ['type', 'data'], ['data']));
const results: Record<string, Rule> = {
    'plugin/load': closed({ skipped_registrations: array(closed({ kind: string(true), id: string(true), reason: string() }, ['kind', 'id', 'reason'])) }, []),
    'plugin/unload': closed({ ok: boolean }, ['ok']),
    'plugin/health': closed({ ok: boolean, message: string() }, ['ok']),
    'command/execute': closed({ action: string(), content: string(), envelopes }, ['action']),
    'event/handle': closed({ cancel: boolean, reason: string(), envelopes }, []),
    'crud/create': closed({ data: object }, ['data'], ['data']),
    'crud/read': closed({ data: object }, ['data'], ['data']),
    'crud/update': closed({ data: object }, ['data'], ['data']),
    'crud/delete': closed({ ok: boolean }, ['ok']),
    'crud/list': closed({ items: array(v => { if (v !== null)
            object(v); }) }, ['items']),
    'mcp/call_tool': closed({ content: () => { }, is_error: boolean, envelopes }, ['content'], ['content']),
    'http/handle': closed({ status: v => { if (typeof v !== 'number' || !Number.isSafeInteger(v))
            fail(); }, headers: strings, body: base64 }, ['status']),
    'plugin/migrate': closed({ notes: array(string()) }, []),
};
export function validateRuntimeResult(method: string, value: unknown, limit = DEFAULT_FRAME_BYTES - 1): void { const raw = encodeBoundedJSON(value, limit); inspectAuthored(value); try {
    results[method]?.(value);
    validateJSON(raw);
}
catch {
    throw new Error('invalid runtime result');
} }
// Optional top-level undefined means omitted; nested values are never erased.
export function authoredResult(method: string, value: unknown, limit = DEFAULT_FRAME_BYTES - 1): Record<string, unknown> {
    encodeBoundedJSON(value, limit, method === 'http/handle');
    if (!record(value))
        throw new Error('expected result object');
    const result: Record<string, unknown> = {};
    if (Object.getPrototypeOf(value) !== Object.prototype && Object.getPrototypeOf(value) !== null || Object.getOwnPropertySymbols(value).length)
        throw new Error('non-JSON result object');
    for (const [k, d] of Object.entries(Object.getOwnPropertyDescriptors(value))) {
        if (!Object.hasOwn(d, 'value') || !d.enumerable)
            throw new Error('non-JSON result property');
        if (d.value !== undefined)
            Object.defineProperty(result, k, { value: d.value, enumerable: true, configurable: true });
    }
    if (method === 'http/handle' && Object.hasOwn(result, 'body')) {
        if (!(result.body instanceof Uint8Array))
            throw new Error('expected byte body');
        Object.defineProperty(result, 'body', { value: Buffer.from(result.body).toString('base64'), enumerable: true });
    }
    validateRuntimeResult(method, result, limit);
    return result;
}
