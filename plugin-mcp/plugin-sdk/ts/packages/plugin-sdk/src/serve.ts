import { decodeRuntimeParams, PayloadError } from './payload.js';
import { Buffer } from 'node:buffer';
import process from 'node:process';
import { Readable } from 'node:stream';
import type { Writable } from 'node:stream';
import { ConfigReader } from './config.js';
import { Dispatcher, decodeRequest } from './dispatch.js';
import { EnvelopeFault } from './envelope.js';
import { errorMessage } from './errors.js';
import { createLogger, SecretTracker } from './log.js';
import type { ServerPlugin } from './types.js';
import type { RPCResponse } from './wire.js';

export const MAX_INPUT_FRAME_BYTES = 8 * 1024 * 1024;
export class FrameTooLargeError extends Error {
  constructor() { super('stdin frame exceeds 8 MiB scanner limit'); this.name = 'FrameTooLargeError'; }
}
export interface ServeOptions {
  input?: Readable;
  output?: Writable;
  stderr?: Writable;
  signal?: AbortSignal;
  /** Total drain, cleanup and flush budget, default 5000 ms. */
  shutdownTimeoutMs?: number;
  /** Default true only when using process stdin. Injectable streams own their signals. */
  handleSignals?: boolean;
}
async function* frames(input: Readable, signal: AbortSignal): AsyncGenerator<string> {
  let parts: Buffer[] = [];
  let length = 0;
  for await (const chunk of chunks(input, signal)) {
    const bytes = typeof chunk === 'string' ? Buffer.from(chunk) : Buffer.from(chunk as Uint8Array);
    let offset = 0;
    while (offset < bytes.length) {
      const end = bytes.indexOf(10, offset);
      const part = bytes.subarray(offset, end < 0 ? bytes.length : end);
      length += part.length;
      if (length >= MAX_INPUT_FRAME_BYTES) throw new FrameTooLargeError();
      parts.push(part);
      if (end < 0) break;
      const line = Buffer.concat(parts, length);
      yield line.subarray(0, line.at(-1) === 13 ? line.length - 1 : line.length).toString('utf8');
      parts = []; length = 0; offset = end + 1;
    }
  }
  if (length) {
    const line = Buffer.concat(parts, length);
    yield line.subarray(0, line.at(-1) === 13 ? line.length - 1 : line.length).toString('utf8');
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
  if (!plugin || typeof plugin.init !== 'function' || typeof plugin.load !== 'function' || typeof plugin.unload !== 'function') throw new Error('serve requires init, load and unload');
  const timeout = options.shutdownTimeoutMs ?? DEFAULT_SHUTDOWN_TIMEOUT_MS;
  if (!Number.isFinite(timeout) || timeout <= 0 || timeout > 2147483647) throw new Error('shutdownTimeoutMs must be positive and at most 2147483647');
  const deno = (globalThis as typeof globalThis & { Deno?: { stdin: { readable: Parameters<typeof Readable.fromWeb>[0] } } }).Deno;
  const input = options.input ?? (deno ? Readable.fromWeb(deno.stdin.readable) : process.stdin);
  const output = options.output ?? process.stdout;
  const controller = new AbortController(), reader = new AbortController();
  const secrets = new SecretTracker();
  const logger = createLogger({secrets,write: line => { (options.stderr ?? process.stderr).write(line); }});
  const dispatcher = new Dispatcher(plugin, {signal: controller.signal, logger, config: new ConfigReader({},secrets)}, secrets);
  const pending = new Set<Promise<void>>();
  let writeTail = Promise.resolve(), barrier = Promise.resolve();
  let transportError: unknown, inputError: unknown;
  let closed = false;
  let stoppedResolve!: () => void;
  const stopped = new Promise<void>(resolve => { stoppedResolve = resolve; });
  const stop = (): void => {
    controller.abort(); reader.abort(); stoppedResolve();
    if (options.input === undefined) input.destroy();
  };
  const onOutputError = (error: Error): void => { transportError ??= error; stop(); };
  const signals = options.handleSignals ?? options.input === undefined;
  if (signals) { process.on('SIGTERM',stop); process.on('SIGINT',stop); }
  options.signal?.addEventListener('abort',stop,{once:true});
  output.on('error',onOutputError);
  if (options.signal?.aborted) stop();
  const write = (response: RPCResponse): Promise<void> => {
    if (closed || transportError) return Promise.resolve();
    let line: string;
    try { line = JSON.stringify(response, (_key, value: unknown) => {
      if (typeof value === 'function' || typeof value === 'symbol' || (typeof value === 'number' && !Number.isFinite(value))) throw new Error('unserializable result value');
      return value;
    }) + '\n'; }
    catch (error) { line = JSON.stringify({jsonrpc:'2.0',id:response.id,error:{code:-32603,message:`marshal result: ${errorMessage(error)}`}}) + '\n'; }
    writeTail = writeTail.then(() => {
      if (closed || transportError) return;
      return new Promise<void>((resolve,reject) => {
        output.write(line, error => error ? reject(error) : resolve());
      });
    }).catch(error => { transportError ??= error; stop(); });
    return writeTail;
  };
  const track = (task: Promise<void>): void => {
    pending.add(task);
    void task.then(() => pending.delete(task), error => { pending.delete(task); inputError ??= error; stop(); });
  };
  let terminal: import('./wire.js').RPCRequest | undefined;
  const pump = (async () => {
    try {
      for await (const line of frames(input,reader.signal)) {
        if (reader.signal.aborted) break;
        try {
          const request = decodeRequest(line);
          if (!request) continue;
          if (request.method === 'plugin/unload') {
            try { decodeRuntimeParams(request); }
            catch(error) {
              if(!(error instanceof PayloadError)) throw error;
              if(request.id!==undefined) void write({jsonrpc:'2.0',id:request.id,error:{code:-32602,message:error.message}});
              continue;
            }
            terminal = request; break;
          }
          // Keep Init admission ordered without blocking reader EOF/cancellation.
          const task = barrier.then(() => dispatcher.dispatch(request)).then(response => { if (response) void write(response); });
          track(task);
          if (request.method === 'plugin/init') barrier = task;
        } catch (error) {
          if (!(error instanceof EnvelopeFault)) throw error;
          void write({jsonrpc:'2.0',id:error.id,error:{code:error.code,message:error.message}});
        }
      }
    } catch (error) { if (!reader.signal.aborted) inputError = error; }
  })();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const cleanupController = new AbortController();
  try {
    await Promise.race([pump,stopped]);
    stop();
    const deadline = new Promise<never>((_resolve,reject) => {
      timer = setTimeout(() => {
        const failure = new ShutdownTimeoutError();
        transportError ??= failure; cleanupController.abort(); reject(failure);
      }, timeout);
    });
    const shutdown = (async () => {
      await Promise.all([...pending]);
      if (closed) throw new ShutdownTimeoutError();
      const forwardContext = terminal && dispatcher.ready ? decodeRuntimeParams<{context?: import('./host-rpc.js').ForwardContext}>(terminal).context : undefined;
      const cleanupContext = {...dispatcher.context,forwardContext,signal:cleanupController.signal};
      let cleanupError: unknown, cleanupFailed = false;
      try { await dispatcher.shutdown(cleanupContext); } catch (error) { cleanupError = error; cleanupFailed = true; }
      if (closed) throw new ShutdownTimeoutError();
      if (terminal) {
        const response = await dispatcher.dispatch(terminal); // Reuses recorded cleanup.
        if (response) await write(response);
      }
      await writeTail;
      if (transportError) throw transportError;
      if (inputError) throw inputError;
      if (cleanupFailed && !terminal) throw cleanupError;
    })();
    await Promise.race([shutdown,deadline]);
  } finally {
    closed = true; reader.abort(); controller.abort(); cleanupController.abort();
    if (timer !== undefined) clearTimeout(timer);
    if (options.input === undefined) input.destroy();
    if (transportError && options.output === undefined) output.destroy();
    if (signals) { process.off('SIGTERM',stop); process.off('SIGINT',stop); }
    options.signal?.removeEventListener('abort',stop);
    output.off('error',onOutputError);
  }
}
