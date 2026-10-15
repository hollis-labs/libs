import { validateJSON } from '../strict-json.js';

/** Lossless-key preflight before JSON.parse. Rejects duplicate escaped keys,
 * oversized input, excessive nesting, and unsafe numeric values. */
export function parseJSON(raw: string): unknown {
  if (Buffer.byteLength(raw) > 1 << 20) throw new Error('manifest exceeds 1048576 bytes');
  validateJSON(raw);
  const parsed: unknown = JSON.parse(raw);
  let at = 0;
  const whitespace = () => { while (at < raw.length && /\s/u.test(raw[at])) at++; };
  const string = (): string => {
    const start = at++;
    while (at < raw.length) {
      const c = raw[at++];
      if (c === '\\') { at++; continue; }
      if (c === '"') return JSON.parse(raw.slice(start, at)) as string;
    }
    throw new Error('invalid JSON string');
  };
  const value = (depth: number, path:string[]=[]): void => {
    if (depth > 64) throw new Error('JSON nesting exceeds 64 levels');
    whitespace();
    if (raw[at] === '{') {
      at++; whitespace();
      if (raw[at] === '}') { at++; return; }
      while (at < raw.length) {
        whitespace(); const key = string();
        whitespace(); at++; value(depth + 1,[...path,key]); whitespace();
        if (raw[at++] === '}') return;
      }
    } else if (raw[at] === '[') {
      at++; whitespace(); if (raw[at] === ']') { at++; return; }
      let index=0;
      while (at < raw.length) { value(depth + 1,[...path,String(index++)]); whitespace(); if (raw[at++] === ']') return; }
    } else if (raw[at] === '"') string();
    else {
      const start = at; while (at < raw.length && !/[\s,}\]]/u.test(raw[at])) at++;
      const literal = raw.slice(start, at);
      if (/^-?\d/u.test(literal)) {
        const integerField=(path.length===1&&['schema_version','protocol'].includes(path[0]))||(path.length===3&&path[0]==='hooks'&&['priority','timeout'].includes(path[2]));
        if(integerField&&!/^-?(?:0|[1-9]\d*)$(?![\s\S])/u.test(literal))throw new Error('invalid integer spelling');
        const n = Number(literal);
        if (!Number.isFinite(n) || (Number.isInteger(n) && !Number.isSafeInteger(n))) throw new Error('unsafe JSON number');
      }
    }
  };
  value(0);
  return parsed;
}
