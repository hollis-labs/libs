import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {decodeGrant,decodeGrantSet,decodeRuntimeIdentity,decodeInitParams,decodeInitResult,validateInitResult,encodeInitParams,encodeGrant,InitError} from '../dist/init-contract.js';
import {validateJSON} from '../dist/strict-json.js';
import {initParams} from './fixtures.js';
for(const file of ['grants','init']) {
 const fixtures=JSON.parse(readFileSync(new URL(`../../../../protocol/v2/fixtures/${file}.json`,import.meta.url),'utf8'));
 for(const f of fixtures) test(`${file}: ${f.name}`,()=>{
  const decode={grant:decodeGrant,grants:decodeGrantSet,runtime:decodeRuntimeIdentity,params:decodeInitParams,result:decodeInitResult}[f.kind];
  if(f.valid) assert.doesNotThrow(()=>decode(f.input)); else assert.throws(()=>decode(f.input), error=>error instanceof InitError && (!f.code || error.code===f.code));
 });
}
test('profile acknowledgement requires an offer',()=>{
 const result={id:'fixture',name:'Fixture',version:'1',description:'',protocol:2,capability_contract:1,reverse_rpc_version:1};
 assert.throws(()=>validateInitResult(initParams(),result),e=>e.code==='profile_mismatch');
 delete result.reverse_rpc_version; assert.doesNotThrow(()=>validateInitResult(initParams({hooks_profile:{hooks_profile_version:1}}),result));
});
test('raw lone surrogates cannot enter identifiers or values',()=>{
 assert.throws(()=>validateJSON('"'+String.fromCharCode(0xd800)+'"'),SyntaxError);
 assert.throws(()=>validateJSON('{"'+String.fromCharCode(0xdc00)+'":1}'),SyntaxError);
});

test('producer encoders reject non-JSON values without silently replacing them',()=>{
 const fixtures=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/grants.json',import.meta.url),'utf8'));
 const grant=JSON.parse(fixtures[0].input);
 for(const scope of [{n:Infinity},{n:NaN},{n:undefined},{identity:undefined},{n:()=>1}]) assert.throws(()=>encodeGrant({...grant,scope}),InitError);
 assert.throws(()=>encodeInitParams({...initParams(),grants:undefined}),InitError);
 assert.throws(()=>encodeInitParams({...initParams(),config:{token:undefined}}),InitError);
 assert.doesNotThrow(()=>encodeInitParams({...initParams(),identity:undefined}));
});
