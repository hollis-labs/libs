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
  /** Default true only when using process stdin. Injectable streams own their signals. */
  handleSignals?: boolean;
}
async function* frames(input: Readable): AsyncGenerator<string> {
  let parts: Buffer[] = [];
  let length = 0;
  for await (const chunk of input) {
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
/** Serve owns neither injected stdout nor stderr: it flushes its writes but does not end them. */
export async function serve(plugin: ServerPlugin, options: ServeOptions = {}): Promise<void> {
  if (!plugin || typeof plugin.init !== 'function' || typeof plugin.load !== 'function' || typeof plugin.unload !== 'function') throw new Error('serve requires init, load and unload');
  // Deno's Node stdin adapter can leave a libuv socket-backed stdin open
  // after the parent half-closes it. Native stdin also supports OS pipe hosts.
  const deno = (globalThis as typeof globalThis & { Deno?: { stdin: { readable: Parameters<typeof Readable.fromWeb>[0] } } }).Deno;
  const input = options.input ?? (deno ? Readable.fromWeb(deno.stdin.readable) : process.stdin);
  const output = options.output ?? process.stdout;
  const controller = new AbortController();
  const secrets = new SecretTracker();
  const logger = createLogger({secrets,write: line => { (options.stderr ?? process.stderr).write(line); }});
  const dispatcher = new Dispatcher(plugin, {signal: controller.signal, logger, config: new ConfigReader({},secrets)}, secrets);
  const pending = new Set<Promise<void>>();
  let writeTail = Promise.resolve();
  let transportError: unknown;
  const onOutputError = (error: Error): void => { transportError ??= error; stop(); };
  const stop = (): void => { controller.abort(); input.destroy(); };
  const signals = options.handleSignals ?? options.input === undefined;
  if (signals) { process.on('SIGTERM',stop); process.on('SIGINT',stop); }
  options.signal?.addEventListener('abort',stop,{once:true});
  output.on('error',onOutputError);
  if (options.signal?.aborted) stop();
  const write = (response: RPCResponse): Promise<void> => {
    let line: string;
    try { line = JSON.stringify(response, (_key, value: unknown) => {
      if (typeof value === 'function' || typeof value === 'symbol' || (typeof value === 'number' && !Number.isFinite(value))) throw new Error('unserializable result value');
      return value;
    }) + '\n'; }
    catch (error) { line = JSON.stringify({jsonrpc:'2.0',id:response.id,error:{code:-32603,message:`marshal result: ${errorMessage(error)}`}}) + '\n'; }
    writeTail = writeTail.then(() => new Promise<void>((resolve,reject) => {
      output.write(line, error => error ? reject(error) : resolve());
    })).catch(error => { transportError ??= error; stop(); });
    return writeTail;
  };
  let inputError: unknown;
  try {
    for await (const line of frames(input)) {
      if (controller.signal.aborted) break;
      try {
        const request = decodeRequest(line);
        if (!request) continue; // Unsolicited reply: no outgoing waiters yet.
        const task = dispatcher.dispatch(request).then(async response => { if (response) await write(response); });
        if(request.method === 'plugin/init') { await task; continue; }
        pending.add(task);
        void task.finally(() => pending.delete(task));
      } catch (error) {
        if (!(error instanceof EnvelopeFault)) throw error;
        await write({jsonrpc:'2.0',id:error.id,error:{code:error.code,message:error.message}});
      }
    }
  } catch (error) { if (!controller.signal.aborted) inputError = error; }
  finally {
    await Promise.all(pending);
    try { await plugin.unload({...dispatcher.context,signal:new AbortController().signal}); }
    finally {
      await writeTail;
      if (signals) { process.off('SIGTERM',stop); process.off('SIGINT',stop); }
      options.signal?.removeEventListener('abort',stop);
      output.off('error',onOutputError);
    }
  }
  if (transportError) throw transportError;
  if (inputError) throw inputError;
}
