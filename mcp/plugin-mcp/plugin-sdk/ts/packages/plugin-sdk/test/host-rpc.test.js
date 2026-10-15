import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { decodeHostRPCDTO, encodeHostRPCDTO, HostRPCValidationError, MAX_HOST_RPC_DTO_BYTES } from '../dist/host-rpc.js';
const corpus = JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/host-rpc.json', import.meta.url), 'utf8'));
for (const vector of corpus.structural) {
    test('host RPC structural: ' + vector.name, () => {
        if (!vector.valid) {
            assert.throws(() => decodeHostRPCDTO(vector.schema, vector.raw), HostRPCValidationError);
            return;
        }
        const decoded = decodeHostRPCDTO(vector.schema, vector.raw);
        const encoded = encodeHostRPCDTO(vector.schema, decoded);
        assert.deepEqual(decodeHostRPCDTO(vector.schema, encoded), decoded);
    });
}
// Host-policy, transport-policy and byte-policy scenarios require future drivers.
test('strict forward and reverse contexts preserve raw-token integer rules', () => {
    for (const raw of [
        '{"binding_id":"b","timeout_ms":1e0,"parent_call":{"request_owner":"host","id":1}}',
        '{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1.0}}',
        '{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":9007199254740992}}',
        '{"binding_id":"b","timeout_ms":1,"parent_call":{"request_owner":"host","id":1,"id":2}}',
        '{"binding_id":"\\ud800","timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}',
    ])
        assert.throws(() => decodeHostRPCDTO('ReverseContext', raw), HostRPCValidationError);
    for (const raw of ['{"timeout_ms":1}', '{"binding_id":"b","timeout_ms":1}'])
        assert.doesNotThrow(() => decodeHostRPCDTO('ForwardContext', raw));
    for (const raw of ['{"timeout_ms":1,"parent_call":{"request_owner":"host","id":1}}', '{"timeout_ms":1,"binding_id":null}', '{"timeout_ms":1,"Timeout_ms":1}'])
        assert.throws(() => decodeHostRPCDTO('ForwardContext', raw), HostRPCValidationError);
});
test('opaque values preserve escaped-key identity across V8 parses', () => {
    const slash = String.fromCharCode(92);
    const first = JSON.stringify({ a: 1, [slash + slash]: 2 });
    const second = JSON.stringify({ a: 1, [slash + '"']: 3 });
    JSON.parse(first); // Prime the affected V8 same-shape object-key cache.
    for (const [raw, key, value] of [[first, slash + slash, 2], [second, slash + '"', 3]]) {
        const v = decodeHostRPCDTO('StorageGetResult', '{"found":true,"revision":"r","value":' + raw + '}');
        assert.deepEqual(Object.keys(v.value), ['a', key]);
        assert.equal(v.value[key], value);
    }
    // Neither prototype setters nor object-parse caches may rename opaque keys.
    const v = decodeHostRPCDTO('StorageGetResult', '{"found":true,"revision":"r","value":{"__proto__":4}}');
    assert.equal(Object.getPrototypeOf(v.value), Object.prototype);
    assert.equal(Object.getOwnPropertyDescriptor(v.value, '__proto__').value, 4);
});
test('producer encoding refuses silent JSON losses and unsupported properties', () => {
    for (const value of [undefined, () => 1, Symbol('x'), 1n, Infinity, NaN, new Date(), { x: undefined }, { toJSON() { return {}; } }, [, ,], Object.assign([], { extra: 1 })]) {
        assert.throws(() => encodeHostRPCDTO('StorageGetResult', { found: true, revision: 'r', value }), HostRPCValidationError);
    }
    let invoked = false;
    const opaque = { get secret() { invoked = true; return 'private'; } };
    assert.throws(() => encodeHostRPCDTO('StorageGetResult', { found: true, revision: 'r', value: opaque }), HostRPCValidationError);
    assert.equal(invoked, false);
    const cyclic = {};
    cyclic.self = cyclic;
    assert.throws(() => encodeHostRPCDTO('StorageGetResult', { found: true, revision: 'r', value: cyclic }), HostRPCValidationError);
    assert.throws(() => encodeHostRPCDTO('StorageGetResult', { found: true, revision: 'r', value: { n: 9007199254740992 } }), HostRPCValidationError);
});
test('DTO budget counts UTF-8 and base64 JSON bytes, not characters', () => {
    for (const raw of [
        '{"found":true,"revision":"r","value":"' + 'a'.repeat(MAX_HOST_RPC_DTO_BYTES) + '"}',
        '{"found":true,"revision":"r","value":"' + '😀'.repeat(MAX_HOST_RPC_DTO_BYTES / 4) + '"}',
    ])
        assert.throws(() => decodeHostRPCDTO('StorageGetResult', raw), HostRPCValidationError);
    assert.throws(() => encodeHostRPCDTO('StorageGetResult', { found: true, revision: 'r', value: 'a'.repeat(MAX_HOST_RPC_DTO_BYTES) }), HostRPCValidationError);
});
test('producer arrays count encoded elements without array-index field names', () => {
    const value = { found: true, revision: 'r', value: Array(90000).fill('x') };
    const raw = encodeHostRPCDTO('StorageGetResult', value);
    assert.ok(Buffer.byteLength(raw) < MAX_HOST_RPC_DTO_BYTES);
    assert.equal(decodeHostRPCDTO('StorageGetResult', raw).value.length, 90000);
});
test('canonical padded base64 checks unused bits and whitespace', () => {
    for (const raw of ['"AB=="', '"AAB="', '"e30"', '"e30=\\n"'])
        assert.throws(() => decodeHostRPCDTO('Base64', raw), HostRPCValidationError);
    for (const raw of ['""', '"e30="', '"AA=="'])
        assert.doesNotThrow(() => decodeHostRPCDTO('Base64', raw));
    assert.doesNotThrow(() => decodeHostRPCDTO('Base64', JSON.stringify('A'.repeat(MAX_HOST_RPC_DTO_BYTES - 4))));
});
test('HTTP header and URL structure is closed without supplying egress policy', () => {
    for (const raw of ['{"name":"X-Test","value":"one\\r\\ntwo"}', '{"name":"Bad Name","value":""}'])
        assert.throws(() => decodeHostRPCDTO('Header', raw), HostRPCValidationError);
    const context = { binding_id: 'b', timeout_ms: 1, parent_call: { request_owner: 'host', id: 1 } };
    for (const url of ['https://user:pass@example.com', 'https://@example.com', 'https:///example.com', 'https://example.com/#fragment', 'http://example.com', 'https:/example.com', 'https://example.com/path with space', 'https://example.com/%ZZ', 'https://example.com:99999/'])
        assert.throws(() => encodeHostRPCDTO('EgressRequestParams', { grant_id: 'g', context, method: 'GET', url }), HostRPCValidationError);
    // A syntactically valid address is not a grant or a destination-policy decision.
    assert.doesNotThrow(() => encodeHostRPCDTO('EgressRequestParams', { grant_id: 'g', context, method: 'GET', url: 'https://example.com/' }));
});
test('log field names are unique exact-case keys', () => {
    const context = { binding_id: 'b', timeout_ms: 1, parent_call: { request_owner: 'host', id: 1 } };
    const value = { grant_id: 'g', context, level: 'info', message: 'ready', fields: [{ name: 'key', value: 1 }, { name: 'key', value: 2 }] };
    assert.throws(() => encodeHostRPCDTO('LogParams', value), HostRPCValidationError);
    value.fields[1].name = 'Key';
    assert.doesNotThrow(() => encodeHostRPCDTO('LogParams', value));
});
test('application errors retain closed leaf metadata and positive correlation',()=>{
 const base='{"contract":"host-rpc/1","code":"target_unavailable","request_id":1,"effect_state":"not_started","retryable":false,"detail":"parent_invalid"}';
 for(const [before,after] of [
  ['"request_id":1','"request_id":null'],['"request_id":1','"request_id":0'],
  ['"request_id":1','"request_id":1e0'],['"request_id":1','"request_id":1.0'],
  ['"request_id":1','"request_id":9007199254740992'],['"request_id":1','"Request_id":1'],
  ['"request_id":1','"request_id":1,"request_id":1'],['"retryable":false','"retryable":true'],
  ['"retryable":false','"retryable":null'],['"parent_invalid"','"parent_unknown"'],['"parent_invalid"','""'],
  ['"target_unavailable"','"unknown_code"'],['"not_started"','"unknown_state"'],
  ['"target_unavailable"','"unknown_outcome"'],['"contract":"host-rpc/1"','"contract":"host-rpc/2"'],
 ])assert.throws(()=>decodeHostRPCDTO('HostRPCErrorData',base.replace(before,after)),HostRPCValidationError);
 assert.doesNotThrow(()=>decodeHostRPCDTO('HostRPCErrorData',base.replace('target_unavailable','unknown_outcome').replace('not_started','unknown')));
 for(const detail of ['stale_binding','callback_cycle','depth_exceeded','parent_invalid','parent_terminal']){
  const data=decodeHostRPCDTO('HostRPCErrorData',base.replace('parent_invalid',detail));
  const response={jsonrpc:'2.0',id:1,error:{code:-32010,message:'safe',data}};
  assert.deepEqual(decodeHostRPCDTO('ApplicationErrorResponse',encodeHostRPCDTO('ApplicationErrorResponse',response)),response);
  response.id=2;assert.throws(()=>encodeHostRPCDTO('ApplicationErrorResponse',response),HostRPCValidationError);
  response.id=1;response.error.message='😀'.repeat(257);assert.throws(()=>encodeHostRPCDTO('ApplicationErrorResponse',response),HostRPCValidationError);
  response.error.message='😀'.repeat(256);assert.doesNotThrow(()=>encodeHostRPCDTO('ApplicationErrorResponse',response));
 }
});
