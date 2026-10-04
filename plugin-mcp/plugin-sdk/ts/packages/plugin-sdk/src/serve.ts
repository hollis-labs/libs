import { Admission, RequestScope, admissionLimits, linkRequestScope } from "./admission.js";
import { decodeRPCControlDTO, requestFailureResponse } from "./request-control.js";
import { requestParamsJSON } from "./payload.js";
import { hookResponseJSON } from './hooks-dispatch.js';
import {Correlation, CorrelationError, replyCandidate} from './correlation.js';
import {FrameWriter, queueLimits} from './publication.js';
import type {QueueLimits} from './publication.js';
import { DEFAULT_FRAME_BYTES, FrameTooLargeError, TruncatedFrameError, FrameUTF8Error, encodeBoundedJSON, frameLimit } from './frame-codec.js';
export { FrameTooLargeError, TruncatedFrameError, FrameUTF8Error, WriteTimeoutError } from './frame-codec.js';
import { decodeRuntimeParams, PayloadError } from './payload.js';
import { Buffer } from 'node:buffer';
import process from 'node:process';
import { Readable } from 'node:stream';
import type { Writable } from 'node:stream';
import { ConfigReader } from './config.js';
import { Dispatcher, decodeRequest } from './dispatch.js';
import { EnvelopeFault } from './envelope.js';
import { createLogger, SecretTracker } from './log.js';
import type { ServerPlugin } from './types.js';
import type { RPCResponse } from './wire.js';

export const MAX_INPUT_FRAME_BYTES = DEFAULT_FRAME_BYTES;
export const MAX_OUTPUT_FRAME_BYTES = DEFAULT_FRAME_BYTES;
export interface ServeOptions {
  admissionLimits?: import('./admission.js').AdmissionLimits;
  queueLimits?: QueueLimits;
  inputFrameBytes?: number;
  outputFrameBytes?: number;
  /** Maximum time for one complete write, default 5000 ms. */
  writeTimeoutMs?: number;
  input?: Readable;
  output?: Writable;
  stderr?: Writable;
  signal?: AbortSignal;
  /** Total drain, cleanup and flush budget, default 5000 ms. */
  shutdownTimeoutMs?: number;
  /** Default true only when using process stdin. Injectable streams own their signals. */
  handleSignals?: boolean;
}
async function* frames(input: Readable, signal: AbortSignal, limit: number): AsyncGenerator<string> {
  let parts: Buffer[] = [];
  let length = 0;
  for await (const chunk of byteChunks(input, signal)) {
    const bytes = chunk;
    let offset = 0;
    while (offset < bytes.length) {
      const end = bytes.indexOf(10, offset);
      const part = bytes.subarray(offset, end < 0 ? bytes.length : end);
      length += part.length;
      if (length >= limit) throw new FrameTooLargeError('input', limit);
      parts.push(Buffer.from(part));
      if (end < 0) break;
      const line = Buffer.concat(parts, length);
      try { yield new TextDecoder('utf-8', {fatal:true,ignoreBOM:true}).decode(line.subarray(0, line.at(-1) === 13 ? line.length - 1 : line.length)); }
      catch { throw new FrameUTF8Error(); }
      parts = []; length = 0; offset = end + 1;
    }
  }
  if (length && !signal.aborted) throw new TruncatedFrameError();
}
// Injectable text streams are encoded in bounded segments. Real transports must
// stay byte streams: a caller decoding bytes first may already erase UTF-8 faults.
async function* byteChunks(input: Readable, signal: AbortSignal): AsyncGenerator<Buffer> {
  for await (const chunk of chunks(input, signal)) {
    if (typeof chunk !== 'string') { yield chunk; continue; }
    for (let offset = 0; offset < chunk.length;) {
      let end = Math.min(offset + 4096, chunk.length);
      const last = chunk.charCodeAt(end - 1);
      if (last >= 0xd800 && last <= 0xdbff && end < chunk.length) end++;
      const text = chunk.slice(offset, end);
      for (let i = 0; i < text.length; i++) {
        const c = text.charCodeAt(i);
        if (c >= 0xd800 && c <= 0xdbff) {
          const low = text.charCodeAt(++i);
          if (!(low >= 0xdc00 && low <= 0xdfff)) throw new FrameUTF8Error();
        } else if (c >= 0xdc00 && c <= 0xdfff) throw new FrameUTF8Error();
      }
      yield Buffer.from(text); offset = end;
    }
  }
}
export const DEFAULT_SHUTDOWN_TIMEOUT_MS = 5000;
export class ShutdownTimeoutError extends Error {
  constructor() { super('shutdown incomplete: deadline exceeded'); this.name = 'ShutdownTimeoutError'; }
}

// Cancellation detaches the reader without destroying the caller's stream.
async function* chunks(input: Readable, signal: AbortSignal): AsyncGenerator<Buffer | string> {
  while (!signal.aborted) {
    const chunk = await new Promise<Buffer | string | null>((resolve, reject) => {
      const cleanup = () => {
        input.off('readable', available); input.off('end', end); input.off('close', end); input.off('error', error);
        signal.removeEventListener('abort', end);
      };
      const end = () => { cleanup(); resolve(null); };
      const error = (failure: Error) => { cleanup(); reject(failure); };
      const available = () => {
        try {
          const value = input.read() as Buffer | string | null;
          if (value !== null) { cleanup(); resolve(value); }
          else if (input.readableEnded || input.destroyed) end();
        } catch (failure) { error(failure as Error); }
      };
      input.on('readable', available); input.once('end', end); input.once('close', end); input.once('error', error);
      signal.addEventListener('abort', end, {once:true});
      if (signal.aborted) end(); else available();
    });
    if (chunk === null) return;
    yield chunk;
  }
}

/** Injected input/output/stderr stay caller-owned; only runtime I/O is interrupted. */
export async function serve(plugin: ServerPlugin, options: ServeOptions = {}): Promise<void> {
 return serveConnection(plugin,options,new Correlation());
}

/** Internal fixture seam; not exported from the author entry points. */
export async function serveConnection(plugin: ServerPlugin, options: ServeOptions, core: Correlation): Promise<void> {
  if (!plugin || typeof plugin.init !== 'function' || typeof plugin.load !== 'function' || typeof plugin.unload !== 'function') throw new Error('serve requires init, load and unload');
  const queues = queueLimits(options.queueLimits);
  const admissionPolicy=admissionLimits(options.admissionLimits);core.reverseSlots=admissionPolicy.reverseSlots;
  const inputLimit = frameLimit(options.inputFrameBytes), outputLimit = frameLimit(options.outputFrameBytes);
  const writeTimeout = options.writeTimeoutMs ?? 5000;
  if (!Number.isFinite(writeTimeout) || writeTimeout <= 0 || writeTimeout > 2147483647) throw new Error('writeTimeoutMs must be positive and at most 2147483647');
  const timeout = options.shutdownTimeoutMs ?? DEFAULT_SHUTDOWN_TIMEOUT_MS;
  if (!Number.isFinite(timeout) || timeout <= 0 || timeout > 2147483647) throw new Error('shutdownTimeoutMs must be positive and at most 2147483647');
  const deno = (globalThis as typeof globalThis & { Deno?: { stdin: { readable: Parameters<typeof Readable.fromWeb>[0] } } }).Deno;
  const input = options.input ?? (deno ? Readable.fromWeb(deno.stdin.readable) : process.stdin);
  const output = options.output ?? process.stdout;
  const controller = new AbortController(), reader = new AbortController();
  const secrets = new SecretTracker();
  const logger = createLogger({secrets,write: line => { (options.stderr ?? process.stderr).write(line); }});
  const dispatcher = new Dispatcher(plugin, {signal: controller.signal, logger, config: new ConfigReader({},secrets)}, secrets, outputLimit);
  const pending = new Set<Promise<void>>();
  let initUsed=false;
  let transportError: unknown, inputError: unknown;
  let closed = false;
  let stoppedResolve!: () => void;
  const stopped = new Promise<void>(resolve => { stoppedResolve = resolve; });
  const stop = (): void => {
    controller.abort(); reader.abort(); stoppedResolve();
    if (options.input === undefined) input.destroy();
  };
  const onOutputError = (error: unknown): void => { transportError ??= error; writer.abort(error); core.close(error); stop(); };
  const signals = options.handleSignals ?? options.input === undefined;
  if (signals) { process.on('SIGTERM',stop); process.on('SIGINT',stop); }
  options.signal?.addEventListener('abort',stop,{once:true});
  output.on('error',onOutputError);
  if (options.signal?.aborted) stop();
  const writer=new FrameWriter(output,writeTimeout,onOutputError,queues);
  core.encode=value=>encodeBoundedJSON(value,outputLimit-1)+'\n';
  core.publish=frame=>writer.publish(frame);
  core.publishCall=(frame,signal,onStart,beforeStart,prepare)=>writer.publish(frame,'ordinary',undefined,{signal,onStart,beforeStart,prepare});
  core.publishControl=frame=>writer.publish(frame,'control');
  const write = (response: RPCResponse, admitted=true): Promise<void> => {
    let line: string;
    try { line = (hookResponseJSON(response,outputLimit - 1) ?? encodeBoundedJSON(response, outputLimit - 1)) + '\n'; }
    catch {
      try { line = core.encode({jsonrpc:'2.0',id:response.id,error:{code:-32603,message:'outbound response rejected'}}); }
      catch (error) { onOutputError(error as Error); core.close(error); return Promise.reject(error); }
    }
    const receipt=writer.publish(line,'control');
    void receipt.then(()=>{if(admitted)core.release(response.id);},error=>{if(admitted)core.release(response.id);core.close(error);onOutputError(error as Error);});
    return receipt;
  };
  const admission=new Admission(writer,core,response=>(hookResponseJSON(response,outputLimit-1)??encodeBoundedJSON(response,outputLimit-1))+'\n',onOutputError,admissionPolicy);
  const send = (response:RPCResponse,scope?:RequestScope):void=>{if(scope)scope.reply(response);else void write(response).catch(()=>{});};
  const track = (task: Promise<void>): void => {
    pending.add(task);
    void task.then(() => pending.delete(task), error => { pending.delete(task); inputError ??= error; stop(); });
  };
  let quiescing=false;
  let terminal: import('./wire.js').RPCRequest | undefined;
  let terminalReady=false;
  let terminalScope:RequestScope|undefined;
  const pump = (async () => {
    try {
      for await (const line of frames(input,reader.signal,inputLimit)) {
        const received=performance.now();
        if (reader.signal.aborted) break;
        try {
          const request = decodeRequest(line);
          if (!request) {if(core.directional)core.reply(line);continue;}
          if(request.method==='rpc/cancel') {
           if(request.id!==undefined){core.admit(request.id);send({jsonrpc:'2.0',id:request.id,error:{code:-32600,message:'rpc/cancel requires a notification'}});continue;}
           try{admission.cancel(decodeRPCControlDTO('CancelParams',requestParamsJSON(request)??'null',core.directional));}catch{/* Invalid notifications have no effect or reply. */}
           continue;
          }
          if(quiescing)continue;
          core.admit(request.id);
          const scope=admission.begin(request.method==='plugin/unload'?{...dispatcher.context,signal:new AbortController().signal}:dispatcher.context,request,received);
          if(!scope){if(request.id!==undefined)send(requestFailureResponse(request.id,'rate_limited','not_started'));continue;}
          const refuse=(response:RPCResponse)=>{scope.reply(response);scope.finish();};
          if (request.method === 'plugin/unload') {
            try { decodeRuntimeParams(request); }
            catch(error) {
              if(!(error instanceof PayloadError)) throw error;
              if(request.id!==undefined) refuse({jsonrpc:'2.0',id:request.id,error:{code:-32602,message:error.message}});else scope.finish();
              continue;
            }
            terminalReady=dispatcher.ready;terminal = request;terminalScope=scope; quiescing=true;controller.abort();stoppedResolve();continue;
          }
          if(request.method==='plugin/init') {
            if(typeof request.id!=='number'||!Number.isSafeInteger(request.id)||request.id<=0) {
              if(request.id!==undefined)refuse({jsonrpc:'2.0',id:request.id,error:{code:-32600,message:'init requires a positive safe integer id'}});else scope.finish();continue;
            }
            if(initUsed){refuse({jsonrpc:'2.0',id:request.id,error:{code:-32600,message:'init already attempted'}});continue;}
            initUsed=true;
          } else if(!dispatcher.ready) {
            if(request.id!==undefined)refuse({jsonrpc:'2.0',id:request.id,error:{code:-32600,message:'successful init required'}});else scope.finish();continue;
          }
          const task=(async()=>{try{if(scope.start()){const response=await dispatcher.dispatch(request,scope.ctx);if(response)send(response,scope);}}finally{scope.finish();await scope.executionDone;}})();
          track(task);
        } catch (error) {
          if (!(error instanceof EnvelopeFault)) throw error;
          if(core.directional&&(error.code===-32700||replyCandidate(line)))throw new CorrelationError();
          if(!quiescing)void write({jsonrpc:'2.0',id:error.id,error:{code:error.code,message:error.message}},false).catch(()=>{});
        }
      }
    } catch (error) { if (!reader.signal.aborted) inputError = error; }
    finally {core.close(inputError??transportError);if(inputError)stop();}
  })();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const cleanupController = new AbortController();
  try {
    await Promise.race([pump,stopped]);
    if(!terminal){stop();core.close(inputError??transportError);}
    const deadline = new Promise<never>((_resolve,reject) => {
      timer = setTimeout(() => {
        const failure = new ShutdownTimeoutError();
        transportError ??= failure; cleanupController.abort(); reject(failure);
      }, timeout);
    });
    const shutdown = (async () => {
      await Promise.all([...pending]);
      if (closed) throw new ShutdownTimeoutError();
      const forwardContext = terminal && terminalReady ? decodeRuntimeParams<{context?: import('./host-rpc.js').ForwardContext}>(terminal).context : undefined;
      const cleanupContext = {...dispatcher.context,forwardContext,signal:cleanupController.signal};
      const terminalCancel=()=>cleanupController.abort(terminalScope?.controller.signal.reason);
      terminalScope?.controller.signal.addEventListener('abort',terminalCancel,{once:true});if(terminalScope?.controller.signal.aborted)terminalCancel();
      if(terminalScope){if(!terminalScope.start())throw new ShutdownTimeoutError();linkRequestScope(terminalScope.ctx,cleanupController.signal,{deadline:Math.min(terminalScope.deadline??Infinity,performance.now()+timeout),binding:terminalScope.binding});}
      let cleanupError: unknown, cleanupFailed = false;
      try { await dispatcher.shutdown(cleanupContext); } catch (error) { cleanupError = error; cleanupFailed = true; }
      if (closed) throw new ShutdownTimeoutError();
      if (terminal) {
        const response = terminalReady ? await dispatcher.dispatch(terminal,terminalScope?.ctx) : terminal.id === undefined ? undefined : {jsonrpc:'2.0' as const,id:terminal.id,error:{code:-32600,message:'successful init required'}}; // Reuses recorded cleanup.
        if(response&&terminalScope){terminalScope.reply(response);terminalScope.finish();await terminalScope.executionDone;}
      }
      await writer.flush();
      if (transportError) throw transportError;
      if (inputError) throw inputError;
      if (cleanupFailed && !terminal) throw cleanupError;
    })();
    await Promise.race([shutdown,deadline]);
  } finally {
    core.close(transportError??inputError);writer.abort(transportError??inputError??new Error('connection closed'));
    closed = true; reader.abort(); controller.abort(); cleanupController.abort();
    if (timer !== undefined) clearTimeout(timer);
    if (options.input === undefined) input.destroy();
    if (transportError && options.output === undefined) output.destroy();
    if (signals) { process.off('SIGTERM',stop); process.off('SIGINT',stop); }
    options.signal?.removeEventListener('abort',stop);
    output.off('error',onOutputError);
  }
}
