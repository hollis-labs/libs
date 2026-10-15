import { Buffer } from 'node:buffer';
import { inspectEnvelope, parseJSONTokens, validatePortableJSON } from './strict-json.js';
import type { RPCID, RPCResponse } from './wire.js';
import type { HostRPCErrorData } from './host-rpc.js';
export type CancelReason = 'caller_cancelled' | 'deadline_exceeded' | 'parent_cancelled' | 'connection_closing';
export interface CancelParams {
    request_owner: 'host' | 'plugin';
    id: RPCID;
    reason: CancelReason;
}
export type BaseFailureCode = Extract<HostRPCErrorData['code'], 'rate_limited' | 'cancelled' | 'deadline_exceeded' | 'budget_exceeded' | 'unknown_outcome'>;
export interface PluginRPCErrorData {
    contract: 'plugin-rpc/2';
    code: BaseFailureCode;
    effect_state: HostRPCErrorData['effect_state'];
    retryable: false;
}
export class TransportCancelledError extends Error {
    readonly reason: CancelReason;
    constructor(reason: CancelReason) { super('RPC transport cancelled'); this.name = 'TransportCancelledError'; this.reason = reason; }
}
export class DeadlineExceededError extends Error {
    constructor() { super('RPC deadline exceeded'); this.name = 'DeadlineExceededError'; }
}
const codes = ['rate_limited', 'cancelled', 'deadline_exceeded', 'budget_exceeded', 'unknown_outcome'];
const reasons = ['caller_cancelled', 'deadline_exceeded', 'parent_cancelled', 'connection_closing'];
export function decodeRPCControlDTO(name: 'CancelParams', raw: string, directional?: boolean): CancelParams;
export function decodeRPCControlDTO(name: 'PluginRPCErrorData', raw: string, directional?: boolean): PluginRPCErrorData;
export function decodeRPCControlDTO(name: 'CancelParams' | 'PluginRPCErrorData', raw: string, directional = false): CancelParams | PluginRPCErrorData {
    if (Buffer.byteLength(raw) > 8388608)
        throw new Error('control DTO too large');
    validatePortableJSON(raw);
    const fields = inspectEnvelope(raw).fields;
    const keys = name === 'CancelParams' ? ['request_owner', 'id', 'reason'] : ['contract', 'code', 'effect_state', 'retryable'];
    if (!fields || fields.size !== keys.length || keys.some(k => !fields.has(k)) || [...fields.keys()].some(k => !keys.includes(k)))
        throw new Error('invalid control DTO');
    const value = parseJSONTokens(raw) as Record<string, unknown>;
    if (name === 'CancelParams') {
        const id = value.id;
        if (!['host', 'plugin'].includes(value.request_owner as string) || !reasons.includes(value.reason as string) || !(typeof id === 'string' || typeof id === 'number' && Number.isSafeInteger(id) && /^-?(0|[1-9][0-9]*)$/.test(fields.get('id')!)) || directional && !(typeof id === 'number' && id > 0))
            throw new Error('invalid cancellation');
    }
    else {
        if (value.contract !== 'plugin-rpc/2' || !codes.includes(value.code as string) || !['not_started', 'not_committed', 'committed', 'unknown'].includes(value.effect_state as string) || value.retryable !== false || value.code === 'unknown_outcome' && value.effect_state !== 'unknown' || value.code === 'rate_limited' && value.effect_state !== 'not_started')
            throw new Error('invalid failure classification');
    }
    return value as unknown as CancelParams | PluginRPCErrorData;
}
export function requestFailureResponse(id: RPCID, code: BaseFailureCode, effect_state: PluginRPCErrorData['effect_state']): RPCResponse {
    const positive = typeof id === 'number' && id > 0;
    const data = positive ? { contract: 'host-rpc/1', code, request_id: id, effect_state, retryable: false } : { contract: 'plugin-rpc/2', code, effect_state, retryable: false };
    return { jsonrpc: '2.0', id, error: { code: positive ? -32010 : -32603, message: code, data } };
}
export class RPCTransportError extends Error {
    readonly code: HostRPCErrorData['code'];
    readonly effect_state: HostRPCErrorData['effect_state'];
    readonly retryable = false;
    readonly request_id: number;
    constructor(code: HostRPCErrorData['code'], effect_state: HostRPCErrorData['effect_state'], request_id: number, cause?: unknown) { super('RPC transport: ' + code, { cause }); this.name = 'RPCTransportError'; this.code = code; this.effect_state = effect_state; this.request_id = request_id; }
}
