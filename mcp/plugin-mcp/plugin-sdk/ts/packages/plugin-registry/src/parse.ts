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
  let invalidIntegerLiteral = false;
  const integerField = (path: string[]): boolean =>
    (path.length === 1 && ["registry_version", "revision"].includes(path[0])) ||
    (path.length === 3 &&
      path[0] === "kinds" &&
      path[2] === "schema_version") ||
    (path.length === 4 &&
      path[0] === "contributions" &&
      path[3] === "schema_version");
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
  const value = (path: string[] = []): void => {
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
        const key = string();
        const folded = foldKey(key);
        if (keys.has(folded)) throw new RegistryError("ErrCollision");
        keys.add(folded);
        whitespace();
        at++;
        value([...path, key]);
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
      let index = 0;
      while (at < raw.length) {
        value([...path, String(index++)]);
        whitespace();
        if (raw[at++] === "]") return;
      }
    } else if (raw[at] === '"') {
      string();
    } else {
      const start = at;
      while (at < raw.length && !/[\s,}\]]/u.test(raw[at])) at++;
      const literal = raw.slice(start, at);
      if (
        integerField(path) &&
        /^-?\d/u.test(literal) &&
        !/^-?(?:0|[1-9]\d*)$/u.test(literal)
      )
        invalidIntegerLiteral = true;
    }
  };
  try {
    value();
  } catch (err) {
    if (err instanceof RegistryError) throw err;
    throw new RegistryError("ErrInvalidContribution");
  }
  // Go rejects legacy discriminator keys before decoding numeric wire fields.
  const legacyKey =
    parsed !== null &&
    typeof parsed === "object" &&
    Object.keys(parsed).some((key) => key.toLowerCase() === "protocol");
  if (invalidIntegerLiteral && !legacyKey)
    throw new RegistryError("ErrInvalidContribution");
  const invalid = validateResponse(parsed);
  if (invalid) throw new RegistryError(invalid);
  return parsed as PluginRegistryResponse;
}
