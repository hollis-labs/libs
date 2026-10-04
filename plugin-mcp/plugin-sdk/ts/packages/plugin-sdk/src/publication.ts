import { Buffer } from 'node:buffer';
import type { Writable } from 'node:stream';
import { WriteTimeoutError } from './frame-codec.js';
export const DEFAULT_QUEUE_FRAMES = 32;
export const DEFAULT_QUEUED_WRITE_BYTES = 8 * 1024 * 1024;
export const TERMINAL_CREDIT_BYTES = 1024;
export class PublicationFullError extends Error {
  constructor() { super('publication capacity exceeded'); this.name = 'PublicationFullError'; }
}
export interface QueueLimits { frames?: number; bytes?: number }
export function queueLimits(value: QueueLimits = {}): {frames:number;bytes:number} {
  const frames=value.frames??DEFAULT_QUEUE_FRAMES, bytes=value.bytes??DEFAULT_QUEUED_WRITE_BYTES;
  if(!Number.isSafeInteger(frames)||frames<1||frames>DEFAULT_QUEUE_FRAMES||!Number.isSafeInteger(bytes)||bytes<1||bytes>DEFAULT_QUEUED_WRITE_BYTES)throw new Error('queue limits must be positive and may only narrow defaults');
  return {frames,bytes};
}
type Publication = {frame:string;bytes:number;resolve:()=>void;reject:(error:unknown)=>void;credit?:TerminalCredit;signal?:AbortSignal;onStart?:()=>void;beforeStart?:()=>unknown;prepare?:()=>string;detach?:()=>void};
type Lane = {queue:Publication[];bytes:number;reservedFrames:number;reservedBytes:number};
export class TerminalCredit {
  readonly writer:FrameWriter;readonly bytes:number;state:'reserved'|'published'|'released'='reserved';
  constructor(writer:FrameWriter,bytes:number){this.writer=writer;this.bytes=bytes;}
  release():void{this.writer.releaseCredit(this);}
}
/** Each lane is independently bounded. One in-progress frame is additional. */
export class FrameWriter {
  private ordinary:Lane={queue:[],bytes:0,reservedFrames:0,reservedBytes:0};
  private control:Lane={queue:[],bytes:0,reservedFrames:0,reservedBytes:0};
  private burst=0;
  private active=false;
  private activeItem?:Publication;
  private failure:unknown;
  private sealed=false;
  private abortActive?:(error:unknown)=>void;
  private readonly drained:Promise<void>;
  private resolveDrained!:()=>void;
  private readonly output:Writable;
  private readonly timeout:number;
  private readonly fence:(error:unknown)=>void;
  private readonly limits:{frames:number;bytes:number};
  constructor(output:Writable,timeout:number,fence:(error:unknown)=>void,limits:QueueLimits={}){
    this.output=output;this.timeout=timeout;this.fence=fence;this.limits=queueLimits(limits);
    this.drained=new Promise(resolve=>{this.resolveDrained=resolve;});
  }
  reserveTerminal(bytes=TERMINAL_CREDIT_BYTES):TerminalCredit{
    if(this.sealed||this.failure)throw this.failure??new Error('connection closed');
    const lane=this.control;
    if(!Number.isSafeInteger(bytes)||bytes<1||lane.queue.length+lane.reservedFrames>=this.limits.frames||bytes>this.limits.bytes-lane.bytes-lane.reservedBytes)throw new PublicationFullError();
    lane.reservedFrames++;lane.reservedBytes+=bytes;return new TerminalCredit(this,bytes);
  }
  releaseCredit(credit:TerminalCredit):void{
    if(credit.writer===this&&credit.state==='reserved'){this.control.reservedFrames--;this.control.reservedBytes-=credit.bytes;credit.state='released';}
  }
  publish(frame:string,laneName:'ordinary'|'control'='ordinary',credit?:TerminalCredit,publication?:{signal:AbortSignal;onStart:()=>void;beforeStart?:()=>unknown;prepare?:()=>string}):Promise<void>{
    if(publication?.signal.aborted)return Promise.reject(publication.signal.reason);
    if(this.sealed||this.failure)return Promise.reject(this.failure??new Error('connection closed'));
    const lane=laneName==='control'?this.control:this.ordinary, size=Buffer.byteLength(frame);
    let frames=lane.queue.length+lane.reservedFrames,bytes=lane.bytes+lane.reservedBytes;
    if(credit){if(laneName!=='control'||credit.writer!==this||credit.state!=='reserved')return Promise.reject(new PublicationFullError());frames--;bytes-=credit.bytes;}
    if(frames>=this.limits.frames||size>this.limits.bytes-bytes)return Promise.reject(new PublicationFullError());
    if(credit){lane.reservedFrames--;lane.reservedBytes-=credit.bytes;credit.state='published';}
    return new Promise((resolve,reject)=>{
 const item:Publication={frame,bytes:size,resolve,reject,credit,signal:publication?.signal,onStart:publication?.onStart,beforeStart:publication?.beforeStart,prepare:publication?.prepare};
 if(publication){const cancel=()=>{const i=lane.queue.indexOf(item);if(i>=0){lane.queue.splice(i,1);lane.bytes-=item.bytes;item.detach?.();item.reject(publication.signal.reason);this.wake();}else if(this.activeItem===item){this.abort(publication.signal.reason);this.fence(publication.signal.reason);}};publication.signal.addEventListener('abort',cancel,{once:true});item.detach=()=>publication.signal.removeEventListener('abort',cancel);}
 lane.queue.push(item);lane.bytes+=size;this.pump();});
  }
  abort(error:unknown):void{
    this.failure??=error;this.sealed=true;
    for(const lane of [this.ordinary,this.control]){const items=lane.queue.splice(0);lane.bytes=0;for(const item of items){item.detach?.();if(item.credit)item.credit.state='released';item.reject(error);}}
    const abort=this.abortActive;this.abortActive=undefined;abort?.(error);this.wake();
  }
  async flush():Promise<void>{this.sealed=true;this.wake();await this.drained;if(this.failure)throw this.failure;}
  private wake():void{if(this.sealed&&!this.active&&!this.ordinary.queue.length&&!this.control.queue.length)this.resolveDrained();}
  private pump():void{
    if(this.active)return;
    const lane=!this.control.queue.length||(this.burst>=4&&this.ordinary.queue.length)?this.ordinary:this.control;
    const item=lane.queue.shift();if(!item){this.wake();return;}
    lane.bytes-=item.bytes;
    this.burst=lane===this.control?Math.min(4,this.burst+1):0;
    let refusal:unknown;
    try{if(item.prepare){const prepared=item.prepare();const bytes=Buffer.byteLength(prepared);if(bytes>item.bytes)throw new PublicationFullError();item.frame=prepared;item.bytes=bytes;}refusal=item.signal?.aborted?item.signal.reason:item.beforeStart?.();}catch(error){refusal=error;}
    if(refusal!==undefined){item.detach?.();item.reject(refusal);this.pump();return;}
    this.active=true;this.activeItem=item;item.onStart?.();let settled=false;
    const finish=(error?:unknown):void=>{
      if(settled)return;settled=true;clearTimeout(timer);this.active=false;this.activeItem=undefined;this.abortActive=undefined;item.detach?.();
      if(item.credit)item.credit.state='released';
      if(error){item.reject(error);this.abort(error);this.fence(error);}else{item.resolve();this.pump();}this.wake();
    };
    this.abortActive=finish;
    const timer=setTimeout(()=>finish(new WriteTimeoutError()),this.timeout);
    try{this.output.write(item.frame,error=>finish(error??undefined));}catch(error){finish(error);}
  }
}
