import {test} from 'node:test';
import assert from 'node:assert/strict';
import {Readable, PassThrough, Writable} from 'node:stream';
import {serve, FrameTooLargeError, FrameUTF8Error, TruncatedFrameError, WriteTimeoutError} from '../dist/index.js';
import {encodeBoundedJSON} from '../dist/frame-codec.js';
import {fixturePlugin, initParams} from './fixtures.js';
const stderr = () => new Writable({write(_b,_e,cb){cb();}});
test('bounded encoder counts UTF-8, escaped strings and base64 before copying',()=>{
 for(const value of ['é\n"',[true,null,3],{x:'a'}]){
  const raw=encodeBoundedJSON(value,100); assert.deepEqual(JSON.parse(raw),value);
  assert.equal(encodeBoundedJSON(value,Buffer.byteLength(raw)),raw);
  assert.throws(()=>encodeBoundedJSON(value,Buffer.byteLength(raw)-1),FrameTooLargeError);
 }
 assert.throws(()=>encodeBoundedJSON('\ud800',100));
 assert.equal(encodeBoundedJSON(new Uint8Array([0,1,2]),6,true),'"AAEC"');
 assert.throws(()=>encodeBoundedJSON(new Uint8Array(10000),10,true),FrameTooLargeError);
});
test('input malformed UTF-8 and EOF fragments fail before dispatch',async()=>{
 for(const [raw,ErrorType] of [[Buffer.from([255,10]),FrameUTF8Error],[Buffer.from('{}'),TruncatedFrameError]]){
  let calls=0,text='';const p={...fixturePlugin('base'),init(){calls++;}};
  await assert.rejects(serve(p,{input:Readable.from([...raw].map(b=>Buffer.from([b]))),output:new Writable({write(b,_e,cb){text+=b;cb();}}),stderr:stderr()}),ErrorType);
  assert.equal(calls,0);assert.equal(text,'');
 }
});
test('blocked output fences without EOF and retains caller ownership',{timeout:2000},async()=>{
 const input=new PassThrough();let callback;
 const output=new Writable({write(_b,_e,cb){callback=cb;}});
 const done=serve(fixturePlugin('base'),{input,output,stderr:stderr(),writeTimeoutMs:20});
 input.write('{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\n');
 await assert.rejects(done,WriteTimeoutError);assert.equal(input.destroyed,false);assert.equal(output.destroyed,false);
 callback();input.destroy();output.destroy();
});
test('partial failed output never emits a fallback response',{timeout:2000},async()=>{
 let writes=0,text='';const failure=new Error('partial output failure');
 const output=new Writable({autoDestroy:false,write(b,_e,cb){writes++;text+=b.subarray(0,1);cb(failure);}});
 await assert.rejects(serve(fixturePlugin('base'),{input:Readable.from(['{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\n']),output,stderr:stderr()}),failure);
 assert.equal(writes,1);assert.equal(text,'{');output.destroy();
});
test('too-small output budget fences without publishing JSON',async()=>{
 let writes=0;
 await assert.rejects(serve(fixturePlugin('base'),{input:Readable.from(['{"jsonrpc":"2.0","id":1,"method":"plugin/health"}\n']),output:new Writable({write(_b,_e,cb){writes++;cb();}}),stderr:stderr(),outputFrameBytes:8}),e=>e instanceof FrameTooLargeError&&e.direction==='output'&&e.limit===8);
 assert.equal(writes,0);
});
test('output includes LF and base64 expansion in its cap',async()=>{
 let text='';const output=new Writable({write(b,_e,cb){text+=b;cb();}});
 const p={...fixturePlugin('base'),httpHandle(){return {status:200,body:new Uint8Array(1024)}}};
 const init=JSON.stringify({jsonrpc:'2.0',id:1,method:'plugin/init',params:initParams()});
 await serve(p,{input:Readable.from([init+'\n'+JSON.stringify({jsonrpc:'2.0',id:2,method:'http/handle',params:{method:'GET',path:'/',headers:{},body:''}})+'\n']),output,stderr:stderr(),outputFrameBytes:512});
 const lines=text.trim().split('\n');assert.equal(JSON.parse(lines[1]).error.code,-32603);for(const line of lines)assert.ok(Buffer.byteLength(line)+1<=512);
});
