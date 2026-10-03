// Inspect tokens before JSON.parse can discard duplicate keys or round numbers.
// Closed DTO decoders consume raw fields; opaque values keep their own casing.
export const MAX_JSON_DEPTH = 128;

function inspect(text: string): Map<string, string> | undefined {
  let at = 0;
  let root: Map<string, string> | undefined;
  const fail = (): never => { throw new SyntaxError('Invalid or ambiguous JSON'); };
  const space = () => { while (/[\x20\t\r\n]/.test(text[at] ?? 'x')) at++; };
  const string = (): string => {
    const start = at;
    if (text[at++] !== '"') fail();
    while (at < text.length) {
      const c = text[at++];
      if (c === '"') return JSON.parse(text.slice(start, at)) as string;
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
      at += token![0].length;
    }
  };
  value(0);
  space();
  if (at !== text.length) fail();
  return root;
}

export function validateJSON(text: string): void { inspect(text); }

// Return unparsed field JSON, preserving numeric tokens for DTO range checks.
// All fields are required/non-null and extra/case-folded spellings are refused.
export function decodeJSONObject(text: string, fields: readonly string[]): Map<string, string> {
  const result = inspect(text);
  if (!result || result.size !== fields.length || fields.some(key => !result.has(key) || result.get(key) === 'null')) {
    throw new SyntaxError('Expected closed JSON object with required non-null fields');
  }
  return result;
}
