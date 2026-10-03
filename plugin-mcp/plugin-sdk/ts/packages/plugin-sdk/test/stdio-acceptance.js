import { initParams } from './fixtures.js';
// Execute with Node; pass the worker runtime and arguments, e.g.
// node test/stdio-acceptance.js deno run --allow-env
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
const [runtime = process.execPath,...args] = process.argv.slice(2);
const worker = fileURLToPath(new URL('./stdio-worker.js',import.meta.url));

async function run(signalStop) {
  const child = spawn(runtime,[...args,worker],{stdio:['pipe','pipe','pipe']});
  const exit = once(child,'exit');
  const out = createInterface({input:child.stdout}); const next = out[Symbol.asyncIterator]();
  let stderr = ''; let waitingResolve; const waiting = new Promise(r=>{waitingResolve=r;});
  child.stderr.on('data',b=>{stderr += b; if(stderr.includes('waiting-for-abort'))waitingResolve();});
  const timeout = setTimeout(()=>child.kill('SIGKILL'),10000);
  const send=(id,method,params)=>child.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');
  const reply=async()=>JSON.parse((await next.next()).value);
  try {
    send(1,'plugin/init',initParams({config:{token:'fixture-secret'}})); assert.equal((await reply()).result.protocol,2);
    send(2,'plugin/load'); assert.equal((await reply()).result.skipped_registrations[0].id,'optional');
    if(signalStop) {
      send(3,'command/execute',{name:'wait-for-abort',args:'',session_id:''}); await waiting; child.kill('SIGTERM');
      assert.equal((await reply()).result.action,'noop');
    } else {
      send(3,'command/execute',{name:'panic',args:'',session_id:''}); assert.equal((await reply()).error.code,-32603);
      send(4,'plugin/health'); assert.deepEqual((await reply()).result,{ok:true});
      send(undefined,'event/handle',{type:'cancelled',source:'host',data:{},pre_hook:false}); child.stdin.end();
    }
    assert.equal((await next.next()).done,true,'unexpected late stdout');
    assert.deepEqual(await exit,[0,null]);
    assert.ok(stderr.includes('unloaded')); assert.ok(stderr.includes('[REDACTED]')); assert.ok(!stderr.includes('fixture-secret'));
  } finally { clearTimeout(timeout); out.close(); if(child.exitCode === null)child.kill('SIGKILL'); }
}
await run(false);
await run(true);
console.log(`stdio EOF, isolation, secret redaction and SIGTERM acceptance PASS: ${runtime} ${args.join(' ')}`);
