// Numbers compare by literal text, matching Go json.Number.
class Numeric {
  constructor(readonly text: string) {}
}
export type Json =
  null | boolean | string | Numeric | Json[] | { [key: string]: Json };
const object = (v: Json): v is { [key: string]: Json } =>
  v !== null &&
  typeof v === "object" &&
  !Array.isArray(v) &&
  !(v instanceof Numeric);
export function parse(text: string, rejectDuplicates = false): Json {
  JSON.parse(text);
  let i = 0;
  const ws = () => {
    while (i < text.length && /\s/.test(text[i]!)) i++;
  };
  const fail = (): never => {
    throw new Error("invalid JSON");
  };
  const string = (): string => {
    const start = i++;
    while (i < text.length) {
      if (text[i] === "\\") {
        i += 2;
        continue;
      }
      if (text[i++] === '"')
        return (JSON.parse(text.slice(start, i)) as string).replace(
          /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g,
          "\uFFFD",
        );
    }
    return fail();
  };
  const value = (): Json => {
    ws();
    const c = text[i];
    if (c === '"') return string();
    if (c === "{") {
      i++;
      ws();
      const out: { [key: string]: Json } = Object.create(null) as {
        [key: string]: Json;
      };
      if (text[i] === "}") {
        i++;
        return out;
      }
      for (;;) {
        ws();
        if (text[i] !== '"') return fail();
        const key = string();
        ws();
        if (text[i++] !== ":" || (rejectDuplicates && Object.hasOwn(out, key)))
          return fail();
        out[key] = value();
        ws();
        const end = text[i++];
        if (end === "}") return out;
        if (end !== ",") return fail();
      }
    }
    if (c === "[") {
      i++;
      ws();
      const out: Json[] = [];
      if (text[i] === "]") {
        i++;
        return out;
      }
      for (;;) {
        out.push(value());
        ws();
        const end = text[i++];
        if (end === "]") return out;
        if (end !== ",") return fail();
      }
    }
    for (const [token, result] of [
      ["true", true],
      ["false", false],
      ["null", null],
    ] as const)
      if (text.startsWith(token, i)) {
        i += token.length;
        return result;
      }
    const n = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(
      text.slice(i),
    );
    if (!n) return fail();
    i += n[0].length;
    return new Numeric(n[0]);
  };
  const result = value();
  ws();
  if (i !== text.length) fail();
  return result;
}
function quote(s: string): string {
  return JSON.stringify(s).replace(
    /[<>&\u2028\u2029]/g,
    (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
  );
}
export function encode(v: Json): string {
  if (v instanceof Numeric) return v.text;
  if (Array.isArray(v)) return "[" + v.map(encode).join(",") + "]";
  if (object(v))
    return (
      "{" +
      Object.keys(v)
        .sort()
        .map((k) => quote(k) + ":" + encode(v[k]!))
        .join(",") +
      "}"
    );
  return typeof v === "string" ? quote(v) : JSON.stringify(v);
}
export function numericLiteral(value: Json): string | undefined {
  return value instanceof Numeric ? value.text : undefined;
}
export function strictDocument(text: string): unknown {
  parse(text, true);
  return JSON.parse(text) as unknown;
}
export function validPointer(p: string): boolean {
  return p === "" || (p.startsWith("/") && !/~(?:[^01]|$)/.test(p));
}
const within = (p: string, allowed: string[]) =>
  allowed.some((a) => a === "" || p === a || p.startsWith(a + "/"));
const token = (s: string) => s.replaceAll("~", "~0").replaceAll("/", "~1");
const parts = (p: string) =>
  p === ""
    ? []
    : p
        .slice(1)
        .split("/")
        .map((s) => s.replaceAll("~1", "/").replaceAll("~0", "~"));
function index(s: string, length: number): number {
  const n = Number(s);
  return Number.isSafeInteger(n) && n >= 0 && n < length && String(n) === s
    ? n
    : -1;
}
export function project(v: Json, paths: string[], path = ""): Json {
  if (within(path, paths)) return v;
  if (Array.isArray(v))
    return v.map((child, i) => {
      const p = path + "/" + i;
      return within(p, paths) || paths.some((a) => a.startsWith(p + "/"))
        ? project(child, paths, p)
        : null;
    });
  if (object(v)) {
    const out: { [key: string]: Json } = Object.create(null) as {
      [key: string]: Json;
    };
    for (const k of Object.keys(v)) {
      const p = path + "/" + token(k);
      if (within(p, paths) || paths.some((a) => a.startsWith(p + "/")))
        out[k] = project(v[k]!, paths, p);
    }
    return out;
  }
  return null;
}
function changes(a: Json, b: Json, path = ""): string[] {
  if (encode(a) === encode(b)) return [];
  if (Array.isArray(a) && Array.isArray(b) && a.length === b.length)
    return a.flatMap((v, i) => changes(v, b[i]!, path + "/" + i));
  if (object(a) && object(b))
    return [...new Set([...Object.keys(a), ...Object.keys(b)])].flatMap((k) => {
      const p = path + "/" + token(k);
      return !Object.hasOwn(a, k) || !Object.hasOwn(b, k)
        ? [p]
        : changes(a[k]!, b[k]!, p);
    });
  return [path];
}
function get(v: Json, p: string): { exists: boolean; value: Json } {
  for (const part of parts(p)) {
    if (Array.isArray(v)) {
      const n = index(part, v.length);
      if (n < 0) return { exists: false, value: null };
      v = v[n]!;
    } else if (object(v) && Object.hasOwn(v, part)) v = v[part]!;
    else return { exists: false, value: null };
  }
  return { exists: true, value: v };
}
function set(v: Json, p: string, next: { exists: boolean; value: Json }): Json {
  const ps = parts(p);
  if (!ps.length) return next.value;
  let parent = v;
  for (const part of ps.slice(0, -1)) {
    if (Array.isArray(parent)) {
      const n = index(part, parent.length);
      if (n < 0) throw new Error("invalid merge");
      parent = parent[n]!;
    } else if (object(parent)) {
      if (!Object.hasOwn(parent, part))
        parent[part] = Object.create(null) as Json;
      parent = parent[part]!;
    } else throw new Error("invalid merge");
  }
  const last = ps.at(-1)!;
  if (Array.isArray(parent)) {
    const n = index(last, parent.length);
    if (n < 0 || !next.exists) throw new Error("invalid merge");
    parent[n] = next.value;
  } else if (object(parent)) {
    if (next.exists) parent[last] = next.value;
    else delete parent[last];
  } else throw new Error("invalid merge");
  return v;
}
export function merge(
  full: string,
  input: string,
  output: string,
  mutable: string[],
  view?: string[],
): string {
  const b = parse(output);
  const diff = changes(parse(input), b);
  if (diff.some((p) => !within(p, mutable) || !within(p, view ?? [""])))
    throw new Error("output changes hidden or immutable path");
  if (view === undefined) return encode(b);
  let merged = parse(full);
  for (const p of diff) merged = set(merged, p, get(b, p));
  return encode(merged);
}
