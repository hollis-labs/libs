import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {Writable} from 'node:stream';
import {FrameWriter,PublicationFullError} from '../dist/publication.js';
const recipe=JSON.parse(await readFile(new URL('../../../../protocol/v2/fixtures/duplex-saturation.json',import.meta.url),'utf8'));
function channel(){const values=[],waiting=[];return {put(v){const resolve=waiting.shift();if(resolve)resolve(v);else values.push(v);},take(){return values.length?Promise.resolve(values.shift()):new Promise(resolve=>waiting.push(resolve));}};}
function rig(t,limits){const started=channel();let callback;const output=new Writable({write(b,_e,cb){callback=cb;started.put(b.toString());}});const writer=new FrameWriter(output,1000,()=>{},limits);t.after(async()=>{writer.abort(new Error('test cleanup'));callback?.();await writer.flush().catch(()=>{});output.destroy();});return {writer,started,release(){const cb=callback;callback=undefined;cb();},publish(frame,lane,credit){const p=writer.publish(frame,lane,credit);void p.catch(()=>{});return p;}};}
for(const [name,policy] of [['frames',recipe.frame_saturation],['bytes',recipe.byte_saturation]])test(`writer lane ${name} saturation`,{timeout:2000},async t=>{
 const r=rig(t,{frames:policy.frames,bytes:policy.bytes});r.publish('h'.repeat(policy.active_frame_bytes??8));await r.started.take();
 const count=Math.min(policy.frames,Math.floor(policy.bytes/policy.frame_bytes));
 for(let i=0;i<count;i++){r.publish('o'.repeat(policy.frame_bytes));r.publish('c'.repeat(policy.frame_bytes),'control');}
 await assert.rejects(r.publish('o'.repeat(policy.frame_bytes)),PublicationFullError);await assert.rejects(r.publish('c'.repeat(policy.frame_bytes),'control'),PublicationFullError);
 assert.equal(r.writer.ordinary.bytes+r.writer.control.bytes,2*count*policy.frame_bytes);
});
for(const direction of recipe.directions)test(`writer ${direction} ordinary progress under sustained controls`,{timeout:2000},async t=>{
 const r=rig(t);r.publish('hold\n');await r.started.take();
 for(let i=0;i<recipe.fairness.ordinary_frames;i++)r.publish('ordinary\n');
 for(let i=0;i<recipe.fairness.control_seed_frames;i++)r.publish('control\n','control');
 let seen=0,burst=0;
 while(seen<recipe.fairness.ordinary_frames){r.release();const frame=await r.started.take();if(frame==='ordinary\n'){assert.ok(burst<=recipe.fairness.control_burst);burst=0;seen++;}else{burst++;assert.ok(burst<=recipe.fairness.control_burst);r.publish('control\n','control');}}
});
test('writer terminal credits reserve before execution and cannot be reused',{timeout:2000},async t=>{
 const p=recipe.terminal_reservation,r=rig(t,{frames:p.lane_frames,bytes:p.lane_bytes});r.publish('hold\n');await r.started.take();
 const count=p.forward+p.reverse+p.control,credits=Array.from({length:count},()=>r.writer.reserveTerminal(p.credit_bytes));
 assert.equal(r.writer.control.reservedFrames,count);assert.ok(count<p.lane_frames);assert.equal(r.writer.control.reservedBytes,count*p.credit_bytes);
 await assert.rejects(r.publish('x'.repeat(p.lane_bytes-(count-1)*p.credit_bytes+1),'control',credits[0]),PublicationFullError);
 const committed=JSON.stringify({contract:'host-rpc/1',code:'budget_exceeded',request_id:1,effect_state:'committed',retryable:false})+'\n';
 r.publish(committed,'control',credits[0]);await assert.rejects(r.publish(committed,'control',credits[0]),PublicationFullError);
 for(const credit of credits.slice(1)){credit.release();credit.release();}
 assert.equal(r.writer.control.reservedFrames,0);assert.equal(r.writer.control.reservedBytes,0);
 r.release();assert.equal(await r.started.take(),committed);
});
test('writer abort settles active and both queued lanes exactly once',{timeout:2000},async t=>{
 const r=rig(t,{frames:32,bytes:1024}),failure=new Error('fenced');let receipts=0;
 const collect=p=>p.then(()=>{receipts++;},error=>{assert.equal(error,failure);receipts++;});
 const first=collect(r.publish('active\n'));await r.started.take();const ordinary=collect(r.publish('ordinary\n')),control=collect(r.publish('control\n','control'));
 r.writer.abort(failure);r.writer.abort(failure);await Promise.all([first,ordinary,control]);assert.equal(receipts,3);
});
