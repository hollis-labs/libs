// Inspect tokens before JSON.parse can discard duplicate keys or round numbers.
// Closed DTO decoders consume raw fields; opaque values keep their own casing.
export const MAX_JSON_DEPTH = 128;

function inspect(text: string, portable = false): Map<string, string> | undefined {
  let at = 0;
  let root: Map<string, string> | undefined;
  const fail = (): never => { throw new SyntaxError('Invalid or ambiguous JSON'); };
  const space = () => { while (/[\x20\t\r\n]/.test(text[at] ?? 'x')) at++; };
  const string = (): string => {
    const start = at;
    if (text[at++] !== '"') fail();
    while (at < text.length) {
      const c = text[at++];
      if (c === '"') {
        const decoded = JSON.parse(text.slice(start, at)) as string;
        for (let i = 0; i < decoded.length; i++) {
          const unit = decoded.charCodeAt(i);
          if (unit >= 0xd800 && unit <= 0xdbff) {
            const low = decoded.charCodeAt(++i);
            if (!(low >= 0xdc00 && low <= 0xdfff)) fail();
          } else if (unit >= 0xdc00 && unit <= 0xdfff) fail();
        }
        return decoded;
      }
      if (c === '\\') at++;
    }
    return fail();
  };
  const value = (depth: number): void => {
    if (depth > MAX_JSON_DEPTH) fail();
    space();
    const c = text[at];
    if (c === '{') {
      at++;
      const fields = new Map<string, string>();
      if (depth === 0) root = fields;
      space();
      if (text[at] !== '}') {
        while (true) {
          space();
          const key = string();
          if (fields.has(key)) fail();
          space();
          if (text[at++] !== ':') fail();
          space();
          const start = at;
          value(depth + 1);
          fields.set(key, text.slice(start, at));
          space();
          if (text[at] !== ',') break;
          at++;
        }
      }
      if (text[at++] !== '}') fail();
    } else if (c === '[') {
      at++;
      space();
      if (text[at] !== ']') {
        while (true) {
          value(depth + 1);
          space();
          if (text[at] !== ',') break;
          at++;
        }
      }
      if (text[at++] !== ']') fail();
    } else if (c === '"') {
      string();
    } else {
      const token = /^(?:true|false|null|-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)/.exec(text.slice(at));
      if (!token) fail();
      const raw = token![0];
      if (portable && /^-?[0-9]/.test(raw)) {
        if (!Number.isFinite(Number(raw))) fail();
        if (!/[.eE]/.test(raw) && (BigInt(raw) > 9007199254740991n || BigInt(raw) < -9007199254740991n)) fail();
      }
      at += raw.length;
    }
  };
  value(0);
  space();
  if (at !== text.length) fail();
  return root;
}

export function validateJSON(text: string): void { inspect(text); }
export function validatePortableJSON(text: string): void { inspect(text, true); }

// Return unparsed field JSON, preserving numeric tokens for DTO range checks.
// All fields are required/non-null and extra/case-folded spellings are refused.
export function decodeJSONObject(text: string, fields: readonly string[], optional: readonly string[] = []): Map<string, string> {
  const result = inspect(text);
  if (!result || fields.some(key => !result.has(key)) || [...result].some(([key, raw]) => (!fields.includes(key) && !optional.includes(key)) || raw === 'null')) {
    throw new SyntaxError('Expected closed JSON object with required non-null fields');
  }
  return result;
}

// Syntax is checked by the envelope decoder first. Scan only top-level fields;
// nested params remain opaque to envelope validation, without recursive traversal.
export function inspectEnvelope(text: string): {fields: Map<string, string> | undefined; duplicates: Set<string>; invalidKeys: boolean} {
  const duplicates = new Set<string>();
  let invalidKeys = false;
  let at = 0;
  const space = () => { while (/[\x20\t\r\n]/.test(text[at] ?? 'x')) at++; };
  const stringEnd = () => {
    at++; // opening quote
    while (at < text.length) {
      const c = text[at++];
      if (c === '"') break;
      if (c === '\\') at++;
    }
  };
  space();
  if (text[at++] !== '{') return {fields: undefined, duplicates, invalidKeys};
  const fields = new Map<string, string>();
  space();
  while (text[at] !== '}') {
    const keyStart = at;
    stringEnd();
    const keyRaw = text.slice(keyStart, at);
    try { validateJSON(keyRaw); } catch { invalidKeys = true; }
    const key = JSON.parse(keyRaw) as string;
    if (fields.has(key)) duplicates.add(key);
    space(); at++; space(); // colon
    const start = at;
    let depth = 0;
    while (at < text.length) {
      const c = text[at];
      if (c === '"') { stringEnd(); continue; }
      if (c === '{' || c === '[') depth++;
      else if (c === '}' || c === ']') { if (depth === 0) break; depth--; }
      else if (c === ',' && depth === 0) break;
      at++;
    }
    fields.set(key, text.slice(start, at).trim());
    if (text[at] !== ',') break;
    at++; space();
  }
  return {fields, duplicates, invalidKeys};
}
