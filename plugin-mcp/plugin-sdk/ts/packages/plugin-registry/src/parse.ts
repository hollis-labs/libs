import type { PluginRegistryResponse } from "./types.js";
import { validateResponse, foldKey } from "./validation.js";

import { RegistryError } from "./error.js";
export { RegistryError } from "./error.js";

/** Parse the original response text. JSON.parse alone loses duplicate keys.
 * The scan checks every object, including opaque metadata and unknown fields.
 * Hosts must use this function (or pass text to sync) before any lossy JSON parse.
 */
export function parseRegistryResponse(raw: string): PluginRegistryResponse {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    throw new RegistryError("ErrInvalidContribution");
  }
  let at = 0;
  const whitespace = () => {
    while (/\s/u.test(raw[at] ?? "") && at < raw.length) at++;
  };
  const string = (): string => {
    const start = at++;
    while (at < raw.length) {
      const ch = raw[at++];
      if (ch === "\\") {
        at++;
        continue;
      }
      if (ch === '"') return JSON.parse(raw.slice(start, at)) as string;
    }
    throw new RegistryError("ErrInvalidContribution");
  };
  const value = (): void => {
    whitespace();
    if (raw[at] === "{") {
      at++;
      whitespace();
      const keys = new Set<string>();
      if (raw[at] === "}") {
        at++;
        return;
      }
      while (at < raw.length) {
        whitespace();
        const key = foldKey(string());
        if (keys.has(key)) throw new RegistryError("ErrCollision");
        keys.add(key);
        whitespace();
        at++;
        value();
        whitespace();
        const end = raw[at++];
        if (end === "}") return;
      }
    } else if (raw[at] === "[") {
      at++;
      whitespace();
      if (raw[at] === "]") {
        at++;
        return;
      }
      while (at < raw.length) {
        value();
        whitespace();
        if (raw[at++] === "]") return;
      }
    } else if (raw[at] === '"') {
      string();
    } else {
      while (at < raw.length && !/[\s,}\]]/u.test(raw[at])) at++;
    }
  };
  try {
    value();
  } catch (err) {
    if (err instanceof RegistryError) throw err;
    throw new RegistryError("ErrInvalidContribution");
  }
  const invalid = validateResponse(parsed);
  if (invalid) throw new RegistryError(invalid);
  return parsed as PluginRegistryResponse;
}
