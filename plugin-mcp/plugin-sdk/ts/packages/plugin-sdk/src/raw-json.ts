import { validateJSON, parseJSONTokens } from './strict-json.js';
const brand: unique symbol = Symbol('RawJSON');
/** Validated opaque JSON literal. This string is spliced, never re-encoded. */
export type RawJSON = string & { readonly [brand]: true };
export function rawJSON(text: string): RawJSON {
  if (typeof text !== 'string') throw new TypeError('RawJSON requires JSON text');
  validateJSON(text);
  const check = (value: unknown): void => {
    if (typeof value === 'number' && !Number.isFinite(value)) throw new TypeError('nonfinite JSON number');
    if (value && typeof value === 'object') for (const v of Object.values(value)) check(v);
  };
  check(parseJSONTokens(text));
  return text as RawJSON;
}
