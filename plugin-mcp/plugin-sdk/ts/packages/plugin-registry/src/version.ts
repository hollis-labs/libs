import type { RegistryRuntime } from "./types.js";
const syntax =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/u;
interface Version {
  numbers: number[];
  pre: string[];
}
export function parseVersion(value: string): Version | undefined {
  const m = syntax.exec(value);
  if (!m) return;
  const numbers = m.slice(1, 4).map(Number);
  if (numbers.some((n) => !Number.isSafeInteger(n))) return;
  const pre = m[4]?.split(".") ?? [];
  if (pre.some((p) => /^\d+$/u.test(p) && p.length > 1 && p.startsWith("0")))
    return;
  return { numbers, pre };
}
export function compare(a: Version, b: Version): number {
  for (let i = 0; i < 3; i++) {
    if (a.numbers[i] !== b.numbers[i])
      return a.numbers[i] < b.numbers[i] ? -1 : 1;
  }
  if (!a.pre.length || !b.pre.length)
    return a.pre.length === b.pre.length ? 0 : a.pre.length ? -1 : 1;
  for (let i = 0; i < Math.min(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i],
      y = b.pre[i];
    if (x === y) continue;
    const xn = /^\d+$/u.test(x),
      yn = /^\d+$/u.test(y);
    if (xn && yn && x.length !== y.length) return x.length < y.length ? -1 : 1;
    if (xn !== yn) return xn ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return Math.sign(a.pre.length - b.pre.length);
}
export function validBounds(min?: string, max?: string): boolean {
  if (!min && !max) return false;
  const a = min ? parseVersion(min) : undefined,
    b = max ? parseVersion(max) : undefined;
  return !((min && !a) || (max && !b)) && (!a || !b || compare(a, b) <= 0);
}
export function checkRuntimes(
  requirements: readonly RegistryRuntime[],
  versions: Readonly<Record<string, string>>,
  allowPrerelease = false,
): boolean {
  return requirements.every((r) => {
    const actual = parseVersion(
      (Object.hasOwn(versions, r.name) ? versions[r.name] : "") ?? "",
    );
    if (
      !actual ||
      !validBounds(r.min, r.max) ||
      (!allowPrerelease && actual.pre.length)
    )
      return false;
    return (
      (!r.min || compare(actual, parseVersion(r.min)!) >= 0) &&
      (!r.max || compare(actual, parseVersion(r.max)!) <= 0)
    );
  });
}
