import { validateJSON } from './strict-json.js';
import { Buffer } from 'node:buffer';
export const DEFAULT_FRAME_BYTES = 8 * 1024 * 1024;
export class FrameTooLargeError extends Error {
  readonly direction: 'input' | 'output';
  readonly limit: number;
  constructor(direction: 'input' | 'output', limit: number) {

    super(`${direction} frame exceeds byte limit ${limit}`); this.direction = direction; this.limit = limit; this.name = 'FrameTooLargeError';
  }
}
export class TruncatedFrameError extends Error {
  constructor() { super('truncated input frame without LF'); this.name = 'TruncatedFrameError'; }
}
export class FrameUTF8Error extends Error {
  constructor() { super('input frame is not valid UTF-8'); this.name = 'FrameUTF8Error'; }
}
export class WriteTimeoutError extends Error {
  constructor() { super('stdout write deadline exceeded'); this.name = 'WriteTimeoutError'; }
}
export function frameLimit(value: number | undefined): number {
  const limit = value ?? DEFAULT_FRAME_BYTES;
  if (!Number.isSafeInteger(limit) || limit < 1 || limit > DEFAULT_FRAME_BYTES) throw new Error('frame limits must be positive and may only narrow defaults');
  return limit;
}
// Internal raw-token seam for profile codecs; not a package export. Validation
// happens before a token can bypass ordinary value encoding. Physical CR/LF
// outside strings are removed to keep exactly one frame per line.
const rawFrames = new WeakMap<object,string>();
export function preserveFrameJSON(raw: string): object {
  validateJSON(raw);
  const marker=Object.freeze({});rawFrames.set(marker,raw.replace(/[\r\n]/g,''));return marker;
}
/** Stage bounded JSON without first making an unbounded serialized copy. */
export function encodeBoundedJSON(value: unknown, limit: number, byteBodies = false): string {
  const pieces: string[] = []; let length = 0, pending = '';
  const put = (text: string): void => {
    const size = Buffer.byteLength(text);
    if (size > limit - length) throw new FrameTooLargeError('output', limit + 1);
    length += size; pending += text;
    if (pending.length >= 4096) { pieces.push(pending); pending = ''; }
  };
  const quote = (text: string): void => {
    if (text.length > limit - length - 2) throw new FrameTooLargeError('output', limit + 1);
    put('"'); let start = 0;
    for (let i = 0; i < text.length; i++) {
      const c = text.charCodeAt(i); let escaped: string | undefined;
      if (c >= 0xd800 && c <= 0xdbff) {
        const next = text.charCodeAt(i + 1);
        if (!(next >= 0xdc00 && next <= 0xdfff)) throw new Error('invalid Unicode string');
        i++;
      } else if (c >= 0xdc00 && c <= 0xdfff) throw new Error('invalid Unicode string');
      else if (c === 34) escaped = '\\"';
      else if (c === 92) escaped = '\\\\';
      else if (c < 32) escaped = ({8:'\\b',9:'\\t',10:'\\n',12:'\\f',13:'\\r'} as Record<number,string>)[c] ?? '\\u' + c.toString(16).padStart(4, '0');
      if (escaped !== undefined) { put(text.slice(start, i)); put(escaped); start = i + 1; }
      else if (i - start >= 4096) { put(text.slice(start, i + 1)); start = i + 1; }
    }
    put(text.slice(start)); put('"');
  };
  const active = new Set<object>();
  const visit = (v: unknown, depth: number): void => {
    if (depth > 128) throw new Error('JSON depth exceeded');
    if(v && typeof v==='object' && rawFrames.has(v)){put(rawFrames.get(v)!);return;}
    if (v === null) { put('null'); return; }
    if (typeof v === 'string') { quote(v); return; }
    if (typeof v === 'boolean') { put(String(v)); return; }
    if (typeof v === 'number' && Number.isFinite(v)) { put(String(v)); return; }
    if (typeof v !== 'object') throw new Error('unserializable result value');
    if (byteBodies && v instanceof Uint8Array) {
      const size = 4 * Math.ceil(v.byteLength / 3);
      if (size + 2 > limit - length) throw new FrameTooLargeError('output', limit + 1);
      quote(Buffer.from(v.buffer, v.byteOffset, v.byteLength).toString('base64')); return;
    }
    if (active.has(v)) throw new Error('cyclic JSON value');
    active.add(v);
    if (Array.isArray(v)) {
      put('[');
      for (let i = 0; i < v.length; i++) {
        if (i) put(','); const descriptor = Object.getOwnPropertyDescriptor(v, String(i));
        if (!descriptor || !Object.hasOwn(descriptor, 'value')) throw new Error('non-JSON array item');
        visit(descriptor.value, depth + 1);
      }
      put(']');
    } else {
      put('{'); let first = true;
      for (const key in v) {
        if (!Object.hasOwn(v, key)) continue;
        const descriptor = Object.getOwnPropertyDescriptor(v, key)!;
        if (!Object.hasOwn(descriptor, 'value')) throw new Error('non-JSON property');
        if (descriptor.value === undefined) continue;
        if (!first) put(','); first = false; quote(key); put(':'); visit(descriptor.value, depth + 1);
      }
      put('}');
    }
    active.delete(v);
  };
  visit(value, 0); if (pending) pieces.push(pending); return pieces.join('');
}
