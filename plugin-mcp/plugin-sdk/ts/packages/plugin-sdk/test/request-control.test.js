import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {decodeRPCControlDTO} from '../dist/request-control.js';
const corpus=JSON.parse(readFileSync(new URL('../../../../protocol/v2/fixtures/duplex-control.json',import.meta.url),'utf8'));
for(const vector of corpus.raw_dtos)test('shared control DTO: '+vector.name,()=>{
 if(vector.valid){const decoded=decodeRPCControlDTO(vector.dto,vector.raw,vector.directional);assert.deepEqual(decodeRPCControlDTO(vector.dto,JSON.stringify(decoded),vector.directional),decoded);}
 else assert.throws(()=>decodeRPCControlDTO(vector.dto,vector.raw,vector.directional));
});
