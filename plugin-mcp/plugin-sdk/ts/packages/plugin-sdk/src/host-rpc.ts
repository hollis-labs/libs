import { Buffer } from 'node:buffer';
import { decodeJSONObject, validatePortableJSON } from './strict-json.js';
// Structure only: hosts own authority, opaque resource schemas and live leases.
export const MAX_HOST_RPC_DTO_BYTES = 1 << 20;
export type BindingID = string;
export class HostRPCValidationError extends Error {
    readonly field: string;
    readonly reason: string;
    constructor(field: string, reason: string) {
        super('host RPC: ' + field + ': ' + reason);
        this.name = 'HostRPCValidationError';
        this.field = field;
        this.reason = reason;
    }
}
function invalid(field: string, reason: string): never { throw new HostRPCValidationError(field, reason); }
export interface ParentCall {
    request_owner: "host" | "plugin";
    id: number;
}
export interface ForwardContext {
    binding_id?: BindingID;
    timeout_ms: number;
}
export interface ReverseContext {
    binding_id: BindingID;
    timeout_ms: number;
    parent_call: ParentCall;
}
export interface HostRPCHeader {
    name: string;
    value: string;
}
export interface HostRPCLogField {
    name: string;
    value: unknown;
}
export interface HostRPCRemainingBudgets {
    timeout_ms: number;
    bytes?: number;
    effects?: number;
    tokens?: number;
}
export interface StorageGetParams {
    grant_id: string;
    context: ReverseContext;
    key: string;
}
export interface StorageGetResult {
    found: boolean;
    value?: unknown;
    revision?: string;
}
export interface StoragePutParams {
    grant_id: string;
    context: ReverseContext;
    key: string;
    value: unknown;
    expected_revision: string | null;
    operation_key: string;
}
export interface StoragePutResult {
    operation_key: string;
    revision: string;
}
export interface StorageDeleteParams {
    grant_id: string;
    context: ReverseContext;
    key: string;
    expected_revision: string;
    operation_key: string;
}
export interface StorageDeleteResult {
    operation_key: string;
    deleted: true;
    revision: string;
}
export interface SecretsGetParams {
    grant_id: string;
    context: ReverseContext;
    secret_ref: string;
}
export interface SecretsGetResult {
    value_base64: string;
    expires_at: string;
}
export interface EgressRequestParams {
    grant_id: string;
    context: ReverseContext;
    method: "GET" | "HEAD" | "OPTIONS" | "POST" | "PUT" | "PATCH" | "DELETE";
    url: string;
    headers?: HostRPCHeader[];
    body_base64?: string;
    operation_key?: string;
}
export interface EgressRequestResult {
    status: number;
    headers: HostRPCHeader[];
    body_base64: string;
    operation_key?: string;
}
export interface EventsPublishParams {
    grant_id: string;
    context: ReverseContext;
    event_name: string;
    payload: unknown;
    operation_key: string;
}
export interface EventsPublishResult {
    operation_key: string;
    accepted: true;
    event_id: string;
}
export interface LogParams {
    grant_id: string;
    context: ReverseContext;
    level: "debug" | "info" | "warn" | "error";
    message: string;
    fields?: HostRPCLogField[];
}
export interface LogResult {
    accepted: boolean;
}
export interface ReadonlyQueryParams {
    grant_id: string;
    context: ReverseContext;
    resource: string;
    schema_version: number;
    params: unknown;
}
export interface ReadonlyQueryResult {
    resource: string;
    schema_version: number;
    data: unknown;
}
export interface MCPListToolsParams {
    grant_id: string;
    context: ReverseContext;
    server_id: string;
    cursor?: string;
}
export interface MCPListToolsResult {
    server_id: string;
    tools: MCPTool[];
    next_cursor?: string;
}
export interface MCPCallToolParams {
    grant_id: string;
    context: ReverseContext;
    server_id: string;
    tool_name: string;
    tool_binding: string;
    arguments: unknown;
    operation_key?: string;
}
export interface MCPCallToolResult {
    content: unknown[];
    is_error: boolean;
    structured_content?: unknown;
    operation_key?: string;
}
export interface MCPCancelCallParams {
    grant_id: string;
    context: ReverseContext;
    target_call_id: number;
}
export interface MCPCancelCallResult {
    accepted: boolean;
    already_terminal: boolean;
}
export interface BindingsRenewParams {
    grant_id: string;
    context: ReverseContext;
    requested_lease_ms: number;
}
export interface BindingsRenewResult {
    binding_id: BindingID;
    expires_at: string;
    remaining_budgets: HostRPCRemainingBudgets;
}
export interface MCPTool {
    tool_name: string;
    description?: string;
    input_schema: unknown;
    output_schema?: unknown;
    effect: "read" | "write" | "destructive";
    tool_binding: string;
}
// TypeScript transport view of the capability leaf's existing vocabulary.
// Keep these exact approved values; no SDK-specific failure classifications.
export const HOST_RPC_ERROR_CODE = -32010 as const;
export const HOST_RPC_FAILURE_CODES = ["invalid_request", "unauthenticated", "capability_denied", "scope_denied", "unsupported_capability", "target_unavailable", "budget_exceeded", "rate_limited", "cancelled", "deadline_exceeded", "unknown_outcome", "internal_error", "conflict"] as const;
export const HOST_RPC_FAILURE_DETAILS = ["stale_binding", "callback_cycle", "depth_exceeded", "parent_invalid", "parent_terminal"] as const;
export const HOST_RPC_EFFECT_STATES = ["not_started", "not_committed", "committed", "unknown"] as const;
export interface HostRPCErrorData {
    contract: 'host-rpc/1';
    code: typeof HOST_RPC_FAILURE_CODES[number];
    request_id: ParentCall['id'];
    effect_state: typeof HOST_RPC_EFFECT_STATES[number];
    retryable: false;
    detail?: typeof HOST_RPC_FAILURE_DETAILS[number];
}
export interface HostRPCError {
    code: typeof HOST_RPC_ERROR_CODE;
    message: string;
    data: HostRPCErrorData;
}
export interface ApplicationErrorResponse {
    jsonrpc: '2.0';
    id: ParentCall['id'];
    error: HostRPCError;
}
export interface HostRPCDTOs {
    HostRPCErrorData: HostRPCErrorData;
    HostRPCError: HostRPCError;
    ApplicationErrorResponse: ApplicationErrorResponse;
    "ParentCall": ParentCall;
    "ForwardContext": ForwardContext;
    "ReverseContext": ReverseContext;
    "Header": HostRPCHeader;
    "LogField": HostRPCLogField;
    "RemainingBudgets": HostRPCRemainingBudgets;
    "StorageGetParams": StorageGetParams;
    "StorageGetResult": StorageGetResult;
    "StoragePutParams": StoragePutParams;
    "StoragePutResult": StoragePutResult;
    "StorageDeleteParams": StorageDeleteParams;
    "StorageDeleteResult": StorageDeleteResult;
    "SecretsGetParams": SecretsGetParams;
    "SecretsGetResult": SecretsGetResult;
    "EgressRequestParams": EgressRequestParams;
    "EgressRequestResult": EgressRequestResult;
    "EventsPublishParams": EventsPublishParams;
    "EventsPublishResult": EventsPublishResult;
    "LogParams": LogParams;
    "LogResult": LogResult;
    "ReadonlyQueryParams": ReadonlyQueryParams;
    "ReadonlyQueryResult": ReadonlyQueryResult;
    "MCPListToolsParams": MCPListToolsParams;
    "MCPListToolsResult": MCPListToolsResult;
    "MCPCallToolParams": MCPCallToolParams;
    "MCPCallToolResult": MCPCallToolResult;
    "MCPCancelCallParams": MCPCancelCallParams;
    "MCPCancelCallResult": MCPCancelCallResult;
    "BindingsRenewParams": BindingsRenewParams;
    "BindingsRenewResult": BindingsRenewResult;
    "MCPTool": MCPTool;
    Base64: string;
}
type Rule = (raw: string, field: string) => void;
function boundedString(max: number, nonblank: boolean): Rule {
    return (raw, field) => {
        const value: unknown = JSON.parse(raw);
        if (typeof value !== 'string' || [...value].length > max || (nonblank && !value.trim()))
            invalid(field, 'invalid bounded string');
    };
}
function integer(min: number, max: number): Rule {
    return (raw, field) => {
        if (!/^(?:0|[1-9][0-9]*)$/.test(raw.trim()))
            invalid(field, 'required decimal integer');
        const n = Number(raw);
        if (!Number.isSafeInteger(n) || n < min || n > max)
            invalid(field, 'integer outside range');
    };
}
function enumeration(...values: string[]): Rule {
    return (raw, field) => { const value: unknown = JSON.parse(raw); if (typeof value !== 'string' || !values.includes(value))
        invalid(field, 'unknown enum value'); };
}
const boolean: Rule = (raw, field) => { if (!['true', 'false'].includes(raw.trim()))
    invalid(field, 'required boolean'); };
const trueValue: Rule = (raw, field) => { if (raw.trim() !== 'true')
    invalid(field, 'required true'); };
const opaque: Rule = (raw, field) => {
    try {
        if (raw.trim() === 'null')
            invalid(field, 'required non-null JSON');
        validatePortableJSON(raw);
    }
    catch {
        invalid(field, 'required portable non-null JSON');
    }
};
const nullable = (rule: Rule): Rule => (raw, field) => { if (raw.trim() !== 'null')
    rule(raw, field); };
const reference = (name: string): Rule => (raw, field) => { shapes[name]!(raw, field); };
// Split a scanner-validated container without parsing away numeric tokens.
function rawItems(raw: string): string[] {
    const text = raw.trim();
    const parts: string[] = [];
    let start = 1, depth = 0, quoted = false, escaped = false;
    for (let i = 1; i < text.length - 1; i++) {
        const c = text[i];
        if (quoted) {
            if (escaped)
                escaped = false;
            else if (c === '\\')
                escaped = true;
            else if (c === '"')
                quoted = false;
        }
        else if (c === '"')
            quoted = true;
        else if (c === '{' || c === '[')
            depth++;
        else if (c === '}' || c === ']')
            depth--;
        else if (c === ',' && depth === 0) {
            parts.push(text.slice(start, i).trim());
            start = i + 1;
        }
    }
    const last = text.slice(start, -1).trim();
    if (last)
        parts.push(last);
    return parts;
}
// Build objects from individual scanner-validated tokens. V8 can reuse an
// incorrect escaped object key across same-shaped JSON.parse calls. Parsing
// keys as strings avoids that cache and preserves host-owned opaque keys.
function parseTokens(raw: string): unknown {
    const text = raw.trim();
    if (text.startsWith('['))
        return rawItems(text).map(parseTokens);
    if (!text.startsWith('{'))
        return JSON.parse(text) as unknown;
    const result: Record<string, unknown> = {};
    for (const member of rawItems(text)) {
        const match = /^("(?:[^"\\]|\\.)*")\s*:/.exec(member)!;
        const key = JSON.parse(match[1]!) as string;
        Object.defineProperty(result, key, { value: parseTokens(member.slice(match[0].length)), enumerable: true, writable: true, configurable: true });
    }
    return result;
}
function fields(raw: string, spec: Record<string, Rule>, optional: readonly string[], nulls: readonly string[], field: string): Map<string, string> {
    try {
        const required = Object.keys(spec).filter(k => !optional.includes(k));
        if (!nulls.length)
            return decodeJSONObject(raw, required, optional);
        // Only put.expected_revision permits structural null. The shared scanner
        // already rejected duplicates/surrogates before these raw members are read.
        if (!raw.trim().startsWith('{'))
            invalid(field, 'required closed object');
        const f = new Map<string, string>();
        for (const member of rawItems(raw)) {
            const m = /^("(?:[^"\\]|\\.)*")\s*:/.exec(member);
            if (!m)
                invalid(field, 'required closed object');
            f.set(JSON.parse(m[1]!) as string, member.slice(m[0].length).trim());
        }
        if (required.some(k => !f.has(k)) || [...f].some(([k, v]) => !Object.hasOwn(spec, k) || (v === 'null' && !nulls.includes(k))))
            invalid(field, 'required closed non-null fields');
        return f;
    }
    catch {
        invalid(field, 'invalid closed object');
    }
}
function object(spec: Record<string, Rule>, optional: readonly string[] = [], nulls: readonly string[] = []): Rule {
    return (raw, field) => { const f = fields(raw, spec, optional, nulls, field); for (const [k, v] of f)
        spec[k]!(v, field + '.' + k); };
}
function array(rule: Rule, max: number): Rule {
    return (raw, field) => { if (!raw.trim().startsWith('['))
        invalid(field, 'required array'); const parts = rawItems(raw); if (parts.length > max)
        invalid(field, 'array exceeds limit'); for (const p of parts)
        rule(p, field); };
}
const timestamp: Rule = (raw, field) => {
    const value: unknown = JSON.parse(raw);
    if (typeof value !== 'string')
        invalid(field, 'invalid UTC timestamp');
    const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
    if (!match)
        invalid(field, 'invalid UTC timestamp');
    const [y, m, d, h, min, sec] = match.slice(1, 7).map(Number);
    const date = new Date(0);
    date.setUTCFullYear(y!, m! - 1, d!);
    date.setUTCHours(h!, min!, sec!, 0);
    if (date.getUTCFullYear() !== y || date.getUTCMonth() + 1 !== m || date.getUTCDate() !== d || date.getUTCHours() !== h || date.getUTCMinutes() !== min || date.getUTCSeconds() !== sec)
        invalid(field, 'invalid UTC timestamp');
};
const base64: Rule = (raw, field) => {
    const value: unknown = JSON.parse(raw);
    if (typeof value !== 'string' || !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value) || Buffer.from(value, 'base64').toString('base64') !== value)
        invalid(field, 'noncanonical padded base64');
};
const header: Rule = (raw, field) => {
    object({ name: boundedString(256, true), value: boundedString(8192, false) })(raw, field);
    const h = parseTokens(raw) as {
        name: string;
        value: string;
    };
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(h.name) || /[\r\n\0]/.test(h.value))
        invalid(field, 'invalid HTTP header');
};
const url: Rule = (raw, field) => {
    boundedString(8192, true)(raw, field);
    const value = JSON.parse(raw) as string;
    try {
        const u = new URL(value);
        const authority = /^https:\/\/([^/?#]+)/.exec(value)?.[1];
        if (!authority || authority.includes('@') || !value.startsWith('https://') || /[\x00-\x20\x7f\\#]/.test(value) || /%(?![0-9a-fA-F]{2})/.test(value) || u.protocol !== 'https:' || !u.hostname || u.username || u.password)
            invalid(field, 'invalid absolute HTTPS URL');
    }
    catch {
        invalid(field, 'required absolute HTTPS URL without credentials or fragment');
    }
};
const logFields: Rule = (raw, field) => {
    array(reference('LogField'), 128)(raw, field);
    const items = parseTokens(raw) as Array<{
        name: string;
    }>;
    if (new Set(items.map(x => x.name)).size !== items.length)
        invalid(field, 'duplicate log field name');
};
const shapes: Record<string, Rule> = {
    HostRPCErrorData: object({
        contract: enumeration('host-rpc/1'), code: enumeration(...HOST_RPC_FAILURE_CODES),
        request_id: reference('SafePositiveInteger'), effect_state: enumeration(...HOST_RPC_EFFECT_STATES),
        retryable: (raw, field) => { if (raw.trim() !== 'false')
            invalid(field, 'automatic retry prohibited'); },
        detail: enumeration(...HOST_RPC_FAILURE_DETAILS),
    }, ['detail']),
    HostRPCError: object({ code: (raw, field) => { if (raw.trim() !== String(HOST_RPC_ERROR_CODE))
            invalid(field, 'required application error code'); }, message: boundedString(256, false), data: reference('HostRPCErrorData') }),
    ApplicationErrorResponse: object({ jsonrpc: enumeration('2.0'), id: reference('SafePositiveInteger'), error: reference('HostRPCError') }),
    Token: boundedString(4096, true), SafePositiveInteger: integer(1, Number.MAX_SAFE_INTEGER), TimeoutMs: integer(1, 4294967295), SchemaVersion: integer(1, 4294967295), OpaqueJSON: opaque, Timestamp: timestamp, Base64: base64, Headers: array(header, 128), LogFields: logFields, MCPContent: array(opaque, 1024),
    "ParentCall": object({ "request_owner": enumeration("host", "plugin"), "id": reference("SafePositiveInteger") }, [], []),
    "ForwardContext": object({ "binding_id": reference("Token"), "timeout_ms": reference("TimeoutMs") }, ["binding_id"], []),
    "ReverseContext": object({ "binding_id": reference("Token"), "timeout_ms": reference("TimeoutMs"), "parent_call": reference("ParentCall") }, [], []),
    "Header": header,
    "LogField": object({ "name": boundedString(256, true), "value": reference("OpaqueJSON") }, [], []),
    "RemainingBudgets": object({ "timeout_ms": reference("TimeoutMs"), "bytes": integer(0, 9007199254740991), "effects": integer(0, 9007199254740991), "tokens": integer(0, 9007199254740991) }, ["bytes", "effects", "tokens"], []),
    "StorageGetParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "key": boundedString(4096, true) }, [], []),
    "StorageGetResult": object({ "found": boolean, "value": reference("OpaqueJSON"), "revision": reference("Token") }, ["value", "revision"], []),
    "StoragePutParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "key": boundedString(4096, true), "value": reference("OpaqueJSON"), "expected_revision": nullable(reference("Token")), "operation_key": reference("Token") }, [], ["expected_revision"]),
    "StoragePutResult": object({ "operation_key": reference("Token"), "revision": reference("Token") }, [], []),
    "StorageDeleteParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "key": boundedString(4096, true), "expected_revision": reference("Token"), "operation_key": reference("Token") }, [], []),
    "StorageDeleteResult": object({ "operation_key": reference("Token"), "deleted": trueValue, "revision": reference("Token") }, [], []),
    "SecretsGetParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "secret_ref": reference("Token") }, [], []),
    "SecretsGetResult": object({ "value_base64": reference("Base64"), "expires_at": reference("Timestamp") }, [], []),
    "EgressRequestParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "method": enumeration("GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"), "url": url, "headers": reference("Headers"), "body_base64": reference("Base64"), "operation_key": reference("Token") }, ["headers", "body_base64", "operation_key"], []),
    "EgressRequestResult": object({ "status": integer(100, 599), "headers": reference("Headers"), "body_base64": reference("Base64"), "operation_key": reference("Token") }, ["operation_key"], []),
    "EventsPublishParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "event_name": boundedString(256, true), "payload": reference("OpaqueJSON"), "operation_key": reference("Token") }, [], []),
    "EventsPublishResult": object({ "operation_key": reference("Token"), "accepted": trueValue, "event_id": reference("Token") }, [], []),
    "LogParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "level": enumeration("debug", "info", "warn", "error"), "message": boundedString(16384, false), "fields": reference("LogFields") }, ["fields"], []),
    "LogResult": object({ "accepted": boolean }, [], []),
    "ReadonlyQueryParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "resource": boundedString(256, true), "schema_version": reference("SchemaVersion"), "params": reference("OpaqueJSON") }, [], []),
    "ReadonlyQueryResult": object({ "resource": boundedString(256, true), "schema_version": reference("SchemaVersion"), "data": reference("OpaqueJSON") }, [], []),
    "MCPListToolsParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "server_id": reference("Token"), "cursor": reference("Token") }, ["cursor"], []),
    "MCPListToolsResult": object({ "server_id": reference("Token"), "tools": array(reference("MCPTool"), 256), "next_cursor": reference("Token") }, ["next_cursor"], []),
    "MCPCallToolParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "server_id": reference("Token"), "tool_name": reference("Token"), "tool_binding": reference("Token"), "arguments": reference("OpaqueJSON"), "operation_key": reference("Token") }, ["operation_key"], []),
    "MCPCallToolResult": object({ "content": reference("MCPContent"), "is_error": boolean, "structured_content": reference("OpaqueJSON"), "operation_key": reference("Token") }, ["structured_content", "operation_key"], []),
    "MCPCancelCallParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "target_call_id": reference("SafePositiveInteger") }, [], []),
    "MCPCancelCallResult": object({ "accepted": boolean, "already_terminal": boolean }, [], []),
    "BindingsRenewParams": object({ "grant_id": reference("Token"), "context": reference("ReverseContext"), "requested_lease_ms": integer(1, 300000) }, [], []),
    "BindingsRenewResult": object({ "binding_id": reference("Token"), "expires_at": reference("Timestamp"), "remaining_budgets": reference("RemainingBudgets") }, [], []),
    "MCPTool": object({ "tool_name": reference("Token"), "description": boundedString(16384, false), "input_schema": reference("OpaqueJSON"), "output_schema": reference("OpaqueJSON"), "effect": enumeration("read", "write", "destructive"), "tool_binding": reference("Token") }, ["description", "output_schema"], []),
};
function refine(name: string, refinement: Rule): void { const base = shapes[name]!; shapes[name] = (raw, field) => { base(raw, field); refinement(raw, field); }; }
refine('HostRPCErrorData', (raw, field) => {
    const v = parseTokens(raw) as HostRPCErrorData;
    if (v.code === 'unknown_outcome' && v.effect_state !== 'unknown')
        invalid(field, 'unknown outcome requires unknown effect state');
});
refine('ApplicationErrorResponse', (raw, field) => {
    const v = parseTokens(raw) as ApplicationErrorResponse;
    if (v.id !== v.error.data.request_id)
        invalid(field, 'request ID correlation mismatch');
});
refine('StorageGetResult', (raw, field) => { const v = parseTokens(raw) as Record<string, unknown>; const value = Object.hasOwn(v, 'value'), revision = Object.hasOwn(v, 'revision'); if (v.found ? (!value || !revision) : (value || revision))
    invalid(field, 'found/value/revision mismatch'); });
refine('EgressRequestParams', (raw, field) => { const v = parseTokens(raw) as Record<string, unknown>; if (['POST', 'PUT', 'PATCH', 'DELETE'].includes(v.method as string) && !Object.hasOwn(v, 'operation_key'))
    invalid(field + '.operation_key', 'required mutation key'); });
refine('MCPCancelCallResult', (raw, field) => { const v = parseTokens(raw) as MCPCancelCallResult; if (v.accepted === v.already_terminal)
    invalid(field, 'exclusive cancel flags required'); });
/** Validation only, never a generic host service call or dispatch API. */
export function decodeHostRPCDTO<N extends keyof HostRPCDTOs>(name: N, raw: string): HostRPCDTOs[N] {
    if (!Object.hasOwn(shapes, name))
        invalid('dto', 'unsupported DTO');
    if (raw.length > MAX_HOST_RPC_DTO_BYTES || Buffer.byteLength(raw, 'utf8') > MAX_HOST_RPC_DTO_BYTES)
        invalid(name, 'encoded DTO exceeds byte limit');
    try {
        validatePortableJSON(raw);
    }
    catch {
        invalid(name, 'invalid or ambiguous portable JSON');
    }
    shapes[name]!(raw, name);
    return parseTokens(raw) as HostRPCDTOs[N];
}
// Reject lossy/non-JSON authored values before JSON.stringify calls toJSON,
// drops undefined fields, replaces NaN, or creates an unbounded escaped copy.
function preflight(value: unknown): void {
    let remaining = MAX_HOST_RPC_DTO_BYTES;
    const active = new Set<object>();
    const inspect = (v: unknown, depth: number): void => {
        if (depth > 128)
            invalid('dto', 'JSON nesting exceeds limit');
        if (v === null) {
            remaining -= 4;
        }
        else if (typeof v === 'string') {
            remaining -= Buffer.byteLength(v, 'utf8') + 2;
        }
        else if (typeof v === 'number') {
            if (!Number.isFinite(v))
                invalid('dto', 'nonfinite number');
            remaining -= String(v).length;
        }
        else if (typeof v === 'boolean') {
            remaining -= v ? 4 : 5;
        }
        else if (typeof v === 'object') {
            if (active.has(v))
                invalid('dto', 'cyclic JSON value');
            if (!Array.isArray(v) && Object.getPrototypeOf(v) !== Object.prototype && Object.getPrototypeOf(v) !== null)
                invalid('dto', 'non-JSON object');
            if (Object.getOwnPropertySymbols(v).length)
                invalid('dto', 'symbol property');
            active.add(v);
            remaining -= 2;
            const entries = Object.getOwnPropertyDescriptors(v);
            for (const [key, d] of Object.entries(entries)) {
                if (Array.isArray(v) && key === 'length')
                    continue;
                if (Array.isArray(v) && (!/^(?:0|[1-9][0-9]*)$/.test(key) || Number(key) >= v.length))
                    invalid('dto', 'extended array');
                if (!d.enumerable)
                    invalid('dto', 'non-JSON property');
                if (!Object.hasOwn(d, 'value'))
                    invalid('dto', 'accessor property');
                remaining -= Array.isArray(v) ? 0 : Buffer.byteLength(key, 'utf8') + 3;
                inspect(d.value, depth + 1);
            }
            if (Array.isArray(v) && Object.keys(entries).length !== v.length + 1)
                invalid('dto', 'sparse or extended array');
            active.delete(v);
        }
        else
            invalid('dto', 'non-JSON value');
        if (remaining < 0)
            invalid('dto', 'DTO exceeds byte limit');
    };
    inspect(value, 0);
}
export function encodeHostRPCDTO<N extends keyof HostRPCDTOs>(name: N, value: HostRPCDTOs[N]): string {
    preflight(value);
    const raw = JSON.stringify(value);
    decodeHostRPCDTO(name, raw);
    return raw;
}
export function decodeParentCall(raw: string): ParentCall { return decodeHostRPCDTO("ParentCall", raw); }
export function decodeForwardContext(raw: string): ForwardContext { return decodeHostRPCDTO("ForwardContext", raw); }
export function decodeReverseContext(raw: string): ReverseContext { return decodeHostRPCDTO("ReverseContext", raw); }
export function decodeHostRPCHeader(raw: string): HostRPCHeader { return decodeHostRPCDTO("Header", raw); }
export function decodeHostRPCLogField(raw: string): HostRPCLogField { return decodeHostRPCDTO("LogField", raw); }
export function decodeHostRPCRemainingBudgets(raw: string): HostRPCRemainingBudgets { return decodeHostRPCDTO("RemainingBudgets", raw); }
export function decodeStorageGetParams(raw: string): StorageGetParams { return decodeHostRPCDTO("StorageGetParams", raw); }
export function decodeStorageGetResult(raw: string): StorageGetResult { return decodeHostRPCDTO("StorageGetResult", raw); }
export function decodeStoragePutParams(raw: string): StoragePutParams { return decodeHostRPCDTO("StoragePutParams", raw); }
export function decodeStoragePutResult(raw: string): StoragePutResult { return decodeHostRPCDTO("StoragePutResult", raw); }
export function decodeStorageDeleteParams(raw: string): StorageDeleteParams { return decodeHostRPCDTO("StorageDeleteParams", raw); }
export function decodeStorageDeleteResult(raw: string): StorageDeleteResult { return decodeHostRPCDTO("StorageDeleteResult", raw); }
export function decodeSecretsGetParams(raw: string): SecretsGetParams { return decodeHostRPCDTO("SecretsGetParams", raw); }
export function decodeSecretsGetResult(raw: string): SecretsGetResult { return decodeHostRPCDTO("SecretsGetResult", raw); }
export function decodeEgressRequestParams(raw: string): EgressRequestParams { return decodeHostRPCDTO("EgressRequestParams", raw); }
export function decodeEgressRequestResult(raw: string): EgressRequestResult { return decodeHostRPCDTO("EgressRequestResult", raw); }
export function decodeEventsPublishParams(raw: string): EventsPublishParams { return decodeHostRPCDTO("EventsPublishParams", raw); }
export function decodeEventsPublishResult(raw: string): EventsPublishResult { return decodeHostRPCDTO("EventsPublishResult", raw); }
export function decodeLogParams(raw: string): LogParams { return decodeHostRPCDTO("LogParams", raw); }
export function decodeLogResult(raw: string): LogResult { return decodeHostRPCDTO("LogResult", raw); }
export function decodeReadonlyQueryParams(raw: string): ReadonlyQueryParams { return decodeHostRPCDTO("ReadonlyQueryParams", raw); }
export function decodeReadonlyQueryResult(raw: string): ReadonlyQueryResult { return decodeHostRPCDTO("ReadonlyQueryResult", raw); }
export function decodeMCPListToolsParams(raw: string): MCPListToolsParams { return decodeHostRPCDTO("MCPListToolsParams", raw); }
export function decodeMCPListToolsResult(raw: string): MCPListToolsResult { return decodeHostRPCDTO("MCPListToolsResult", raw); }
export function decodeMCPCallToolParams(raw: string): MCPCallToolParams { return decodeHostRPCDTO("MCPCallToolParams", raw); }
export function decodeMCPCallToolResult(raw: string): MCPCallToolResult { return decodeHostRPCDTO("MCPCallToolResult", raw); }
export function decodeMCPCancelCallParams(raw: string): MCPCancelCallParams { return decodeHostRPCDTO("MCPCancelCallParams", raw); }
export function decodeMCPCancelCallResult(raw: string): MCPCancelCallResult { return decodeHostRPCDTO("MCPCancelCallResult", raw); }
export function decodeBindingsRenewParams(raw: string): BindingsRenewParams { return decodeHostRPCDTO("BindingsRenewParams", raw); }
export function decodeBindingsRenewResult(raw: string): BindingsRenewResult { return decodeHostRPCDTO("BindingsRenewResult", raw); }
export function decodeMCPTool(raw: string): MCPTool { return decodeHostRPCDTO("MCPTool", raw); }
export function decodeHostRPCErrorData(raw: string): HostRPCErrorData { return decodeHostRPCDTO('HostRPCErrorData', raw); }
export function decodeHostRPCError(raw: string): HostRPCError { return decodeHostRPCDTO('HostRPCError', raw); }
export function decodeApplicationErrorResponse(raw: string): ApplicationErrorResponse { return decodeHostRPCDTO('ApplicationErrorResponse', raw); }
