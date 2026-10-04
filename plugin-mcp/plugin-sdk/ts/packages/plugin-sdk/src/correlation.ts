import { decodeHostRPCDTO } from './host-rpc.js';
import type { HostRPCDTOs } from './host-rpc.js';
import { inspectEnvelope, parseJSONTokens, validatePortableJSON } from './strict-json.js';
import type { RPCID } from './wire.js';
export class CorrelationError extends Error {
    constructor() { super('invalid correlation'); this.name = 'CorrelationError'; }
}
export const CORE_CAPACITY = 256;
type DTO = keyof HostRPCDTOs;
const methods: Record<string, [
    DTO,
    DTO
]> = {
    'host/storage/get': ['StorageGetParams', 'StorageGetResult'], 'host/storage/put': ['StoragePutParams', 'StoragePutResult'], 'host/storage/delete': ['StorageDeleteParams', 'StorageDeleteResult'], 'host/secrets/get': ['SecretsGetParams', 'SecretsGetResult'], 'host/egress/request': ['EgressRequestParams', 'EgressRequestResult'], 'host/events/publish': ['EventsPublishParams', 'EventsPublishResult'], 'host/log': ['LogParams', 'LogResult'], 'host/readonly/query': ['ReadonlyQueryParams', 'ReadonlyQueryResult'], 'host/mcp/list_tools': ['MCPListToolsParams', 'MCPListToolsResult'], 'host/mcp/call_tool': ['MCPCallToolParams', 'MCPCallToolResult'], 'host/mcp/cancel_call': ['MCPCancelCallParams', 'MCPCancelCallResult'], 'host/bindings/renew': ['BindingsRenewParams', 'BindingsRenewResult'],
};
type Pending = {
    dto: DTO;
    resolve: (result: unknown) => void;
    reject: (error: unknown) => void;
};
/** Internal engine, intentionally absent from the package's author exports. */
export class Correlation {
    readonly incoming = new Set<RPCID>();
    private pending = new Map<number, Pending>();
    private next = 0;
    private high = 0;
    private failure: unknown;
    publish!: (frame: string) => Promise<void>;
    encode!: (value: unknown) => string;
    readonly directional: boolean;
    constructor(directional = false) { this.directional = directional; }
    admit(id: RPCID | undefined): void {
        if (this.failure)
            throw this.failure;
        if (id === undefined)
            return;
        if (this.incoming.has(id) || this.incoming.size >= CORE_CAPACITY)
            throw new CorrelationError();
        if (this.directional) {
            if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= this.high || id <= 0)
                throw new CorrelationError();
            this.high = id;
        }
        this.incoming.add(id);
    }
    release(id: RPCID | null | undefined): void { if (id !== null && id !== undefined)
        this.incoming.delete(id); }
    close(error: unknown = new Error('connection closed')): void {
        if (this.failure)
            return;
        this.failure = error;
        for (const entry of this.pending.values())
            entry.reject(error);
        this.pending.clear();
        this.incoming.clear();
    }
    call(method: string, paramsRaw: string): Promise<unknown> {
        const pair = Object.hasOwn(methods, method) ? methods[method] : undefined;
        if (!pair || !this.directional || this.failure || this.pending.size >= CORE_CAPACITY || this.next === Number.MAX_SAFE_INTEGER)
            return Promise.reject(this.failure ?? new CorrelationError());
        let params: unknown;
        try {
            params = decodeHostRPCDTO(pair[0], paramsRaw);
        }
        catch (error) {
            return Promise.reject(error);
        }
        const id = ++this.next;
        return new Promise((resolve, reject) => {
            const entry = { dto: pair[1], resolve, reject };
            this.pending.set(id, entry);
            const fail = (error: unknown) => { if (this.pending.get(id) === entry) {
                this.pending.delete(id);
                reject(error);
            } };
            try {
                const frame = this.encode({ jsonrpc: '2.0', id, method, params });
                void this.publish(frame).catch(fail);
            }
            catch (error) {
                fail(error);
            }
        });
    }
    reply(line: string): void {
        validatePortableJSON(line);
        const fields = inspectEnvelope(line).fields!;
        if ([...fields.keys()].some(k => !['jsonrpc', 'id', 'result', 'error'].includes(k)))
            throw new CorrelationError();
        if (fields.has('error')) {
            const raw = fields.get('error')!;
            if ((parseJSONTokens(raw) as {
                code: number;
            }).code === -32010)
                decodeHostRPCDTO('ApplicationErrorResponse', line);
            const e = inspectEnvelope(raw);
            if (e.duplicates.size || e.invalidKeys || !e.fields || [...e.fields.keys()].some(k => !['code', 'message', 'data'].includes(k)))
                throw new CorrelationError();
        }
        const id = parseJSONTokens(fields.get('id')!);
        if (typeof id !== 'number')
            return;
        const entry = this.pending.get(id);
        if (!entry)
            return;
        let value: unknown, error: unknown;
        try {
            if (fields.has('result'))
                value = decodeHostRPCDTO(entry.dto, fields.get('result')!);
            else {
                const raw = fields.get('error')!;
                validatePortableJSON(raw);
                const e = inspectEnvelope(raw);
                if (e.duplicates.size || e.invalidKeys || !e.fields || [...e.fields.keys()].some(k => !['code', 'message', 'data'].includes(k)))
                    throw new CorrelationError();
                const fault = parseJSONTokens(raw) as {
                    code: number;
                    message: string;
                };
                if (fault.code === -32010)
                    decodeHostRPCDTO('ApplicationErrorResponse', line);
                error = fault;
            }
        }
        catch {
            throw new CorrelationError();
        }
        this.pending.delete(id);
        if (error)
            entry.reject(error);
        else
            entry.resolve(value);
    }
}
export function replyCandidate(line: string): boolean {
    try {
        const fields = inspectEnvelope(line).fields;
        return fields?.has('result') === true || fields?.has('error') === true;
    }
    catch {
        return true;
    }
}
