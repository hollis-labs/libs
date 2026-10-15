import { serve } from '../dist/index.js';
import { fixturePlugin } from './fixtures.js';
import { createInterface } from 'node:readline';
import { PassThrough, Writable } from 'node:stream';

// NON-SDK fake-reply lane, confined to this test child. Named raw directives
// replace a reply AFTER SDK serialization; never claim them as SDK behaviour.
const rawResult = (name,id) => {
  switch(name) {
    case 'unknown-status': return {invocation_id:id,status:'success'};
    case 'unknown-code': return {invocation_id:id,status:'failed',error:{code:'cancelled'}};
    case 'action-output': return {invocation_id:id,status:'ok',payload:null};
    case 'non-ok-output': return {invocation_id:id,status:'failed',payload:{},error:{code:'handler_error'}};
    case 'wrong-id': return {invocation_id:'wrong',status:'ok'};
  }
};
const replies = new Map();
const input = new PassThrough();
const lines = createInterface({input:process.stdin,crlfDelay:Infinity});
lines.on('line',line=>{
  try {
    const q=JSON.parse(line),directive=q.params?.metadata?.fixture;
    if(q.method==='hook/handle' && Number.isSafeInteger(q.id) && directive?.startsWith('raw:')) {
      const result=rawResult(directive.slice(4),q.params.invocation_id);
      if(result) replies.set(q.id,result);
    }
  } catch { /* Serve owns validation. */ }
  if(!input.write(line+'\n')) lines.pause();
});
input.on('drain',()=>lines.resume());
lines.on('close',()=>input.end());
const output=new Writable({write(chunk,encoding,done){
  try {
    const response=JSON.parse(chunk.toString());
    if(replies.has(response.id)) {
      chunk=JSON.stringify({jsonrpc:'2.0',id:response.id,result:replies.get(response.id)})+'\n';
      replies.delete(response.id);
    }
  } catch { /* Preserve normal SDK output. */ }
  process.stdout.write(chunk,done);
}});
await serve(fixturePlugin(process.argv[2]),{input,output,handleSignals:true});
