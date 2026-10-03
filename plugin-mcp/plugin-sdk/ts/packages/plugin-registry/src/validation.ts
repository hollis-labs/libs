import { REGISTRY_VERSION, qualifiedKey } from "./types.js";
import type {
  PluginRegistryResponse,
  RegistryContribution,
  KindDescriptor,
  RegionDescriptor,
  Refusal,
} from "./types.js";
import { RegistryError } from "./error.js";
import { validBounds } from "./version.js";
export const record = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" &&
  v !== null &&
  !Array.isArray(v) &&
  (Object.getPrototypeOf(v) === Object.prototype ||
    Object.getPrototypeOf(v) === null);
export const own = <T>(
  map: Readonly<Record<string, T>>,
  key: string,
): T | undefined => (Object.hasOwn(map, key) ? map[key] : undefined);
const nonempty = (v: unknown): v is string =>
  typeof v === "string" && v.length > 0;
const integer = (v: unknown): v is number =>
  typeof v === "number" && Number.isSafeInteger(v) && v > 0;
const name = (v: unknown): v is string =>
  typeof v === "string" && /^[A-Za-z0-9_.-]+$/u.test(v);
const strings = (v: unknown): v is string[] =>
  Array.isArray(v) && Array.from(v).every(nonempty);
const reps = (v: unknown): boolean =>
  Array.isArray(v) &&
  v.length > 0 &&
  new Set(v).size === v.length &&
  v.every((r) => ["component", "declarative", "handler"].includes(r));
const json = (v: unknown): boolean => {
  if (v === null || typeof v === "string" || typeof v === "boolean")
    return true;
  if (typeof v === "number") return Number.isFinite(v);
  if (Array.isArray(v)) return Array.from(v).every(json);
  if (record(v)) return Object.values(v).every(json);
  return false;
};
/** Match Go strings.ToLower: scalar lowercase, without contextual sigma or expansion. */
export const foldKey = (key: string): string =>
  Array.from(key, (char) =>
    char === "\u0130" ? "i" : char.toLowerCase(),
  ).join("");
const exactKeys = (v: Record<string, unknown>, keys: string[]) =>
  Object.keys(v).every(
    (k) =>
      !keys.includes(k.toLowerCase().replaceAll("\u017f", "s")) ||
      keys.includes(k),
  );
function validate(value: unknown): string | undefined {
  if (
    !record(value) ||
    Object.keys(value).some(
      (key) => key.toLowerCase().replaceAll("\u017f", "s") === "protocol",
    )
  )
    return "ErrRegistryVersion";
  if (!exactKeys(value, ["registry_version"])) return "ErrInvalidContribution";
  if (
    !Object.hasOwn(value, "registry_version") ||
    value.registry_version !== REGISTRY_VERSION
  )
    return "ErrRegistryVersion";
  if (
    !exactKeys(value, [
      "registry_version",
      "host_instance",
      "revision",
      "plugins",
      "kinds",
      "regions",
      "contributions",
      "refusals",
    ]) ||
    ![
      "host_instance",
      "revision",
      "plugins",
      "kinds",
      "regions",
      "contributions",
      "refusals",
    ].every((k) => Object.hasOwn(value, k)) ||
    !nonempty(value.host_instance) ||
    !integer(value.revision) ||
    !record(value.plugins) ||
    !record(value.kinds) ||
    !record(value.regions) ||
    !record(value.contributions) ||
    !Array.isArray(value.refusals)
  )
    return "ErrInvalidContribution";
  for (const [id, p] of Object.entries(value.plugins)) {
    if (
      !name(id) ||
      !record(p) ||
      !exactKeys(p, [
        "owner_generation",
        "bundle_url",
        "bundle_version",
        "stylesheet_url",
        "runtime",
      ]) ||
      !nonempty(p.owner_generation)
    )
      return "ErrInvalidContribution";
    if (
      (p.bundle_url !== undefined && !nonempty(p.bundle_url)) ||
      (p.stylesheet_url !== undefined && !nonempty(p.stylesheet_url))
    )
      return "ErrInvalidContribution";
    if (
      !!p.bundle_url !== !!p.bundle_version ||
      (p.bundle_version !== undefined &&
        (typeof p.bundle_version !== "string" ||
          !/^sha256:[a-f0-9]{64}$/u.test(p.bundle_version)))
    )
      return "ErrIntegrity";
    if (p.runtime !== undefined) {
      if (!Array.isArray(p.runtime)) return "ErrRuntime";
      const seen = new Set<string>();
      for (const rt of p.runtime) {
        if (
          !record(rt) ||
          !exactKeys(rt, ["name", "min", "max"]) ||
          !nonempty(rt.name) ||
          seen.has(rt.name) ||
          (rt.min !== undefined && !nonempty(rt.min)) ||
          (rt.max !== undefined && !nonempty(rt.max)) ||
          !validBounds(
            rt.min as string | undefined,
            rt.max as string | undefined,
          )
        )
          return "ErrRuntime";
        seen.add(rt.name);
      }
    }
  }
  for (const [kind, d] of Object.entries(value.kinds)) {
    if (
      !nonempty(kind) ||
      !record(d) ||
      !exactKeys(d, [
        "schema_version",
        "metadata_schema",
        "representations",
        "regions",
        "required_capabilities",
      ]) ||
      !integer(d.schema_version) ||
      !json(d.metadata_schema) ||
      !reps(d.representations) ||
      !strings(d.regions) ||
      !strings(d.required_capabilities)
    )
      return "ErrInvalidContribution";
  }
  for (const [region, d] of Object.entries(value.regions)) {
    if (
      !nonempty(region) ||
      !record(d) ||
      !exactKeys(d, [
        "kinds",
        "representations",
        "context_schema",
        "ordering",
      ]) ||
      !strings(d.kinds) ||
      !reps(d.representations) ||
      !json(d.context_schema) ||
      !["priority-ascending", "priority-descending", "manifest"].includes(
        String(d.ordering),
      )
    )
      return "ErrInvalidContribution";
  }
  const bindings = new Set<string>();
  for (const [kind, entries] of Object.entries(value.contributions)) {
    if (!nonempty(kind) || !record(entries)) return "ErrInvalidContribution";
    for (const [key, c] of Object.entries(entries)) {
      if (
        !record(c) ||
        !exactKeys(c, [
          "owner_id",
          "owner_generation",
          "local_key",
          "kind",
          "schema_version",
          "required",
          "status",
          "status_reason",
          "representation",
          "metadata",
          "component",
          "declarative",
          "handler",
          "public_binding",
        ]) ||
        ![
          "owner_id",
          "owner_generation",
          "local_key",
          "kind",
          "schema_version",
          "required",
          "status",
          "representation",
          "metadata",
        ].every((k) => Object.hasOwn(c, k)) ||
        c.kind !== kind ||
        !name(c.owner_id) ||
        !name(c.local_key) ||
        key !== qualifiedKey(c.owner_id, c.local_key) ||
        !nonempty(c.owner_generation) ||
        !integer(c.schema_version) ||
        typeof c.required !== "boolean" ||
        !nonempty(c.status) ||
        (c.status_reason !== undefined && !nonempty(c.status_reason)) ||
        !json(c.metadata)
      )
        return "ErrInvalidContribution";
      const p = own(value.plugins, c.owner_id);
      if (!record(p)) return "ErrUnknownPlugin";
      if (p.owner_generation !== c.owner_generation)
        return "ErrInvalidContribution";
      if (c.representation === "component") {
        if (
          !record(c.component) ||
          !exactKeys(c.component, ["export", "region"]) ||
          !nonempty(c.component.export) ||
          !nonempty(c.component.region) ||
          c.declarative !== undefined ||
          c.handler !== undefined ||
          !p.bundle_url
        )
          return "ErrInvalidContribution";
      } else if (c.representation === "declarative") {
        if (
          !json(c.declarative) ||
          c.component !== undefined ||
          c.handler !== undefined
        )
          return "ErrInvalidContribution";
      } else if (c.representation === "handler") {
        if (
          !record(c.handler) ||
          !exactKeys(c.handler, ["id"]) ||
          !nonempty(c.handler.id) ||
          c.component !== undefined ||
          c.declarative !== undefined
        )
          return "ErrInvalidContribution";
      } else return "ErrInvalidContribution";
      if (c.public_binding !== undefined) {
        if (!nonempty(c.public_binding)) return "ErrInvalidContribution";
        const binding = JSON.stringify([kind, c.public_binding]);
        if (bindings.has(binding)) return "ErrCollision";
        bindings.add(binding);
      }
    }
  }
  for (const f of value.refusals)
    if (
      !record(f) ||
      !exactKeys(f, [
        "owner_id",
        "owner_generation",
        "kind",
        "local_key",
        "reason",
        "required",
      ]) ||
      !nonempty(f.owner_id) ||
      !nonempty(f.owner_generation) ||
      !nonempty(f.kind) ||
      !nonempty(f.local_key) ||
      !nonempty(f.reason) ||
      typeof f.required !== "boolean"
    )
      return "ErrInvalidContribution";
  const refusalIdentity = (c: Record<string, unknown>) =>
    JSON.stringify([c.owner_id, c.owner_generation, c.kind, c.local_key]);
  const byEntry = new Map<string, Record<string, unknown>>();
  for (const f of value.refusals) {
    const id = refusalIdentity(f as Record<string, unknown>);
    if (byEntry.has(id)) return "ErrCollision";
    byEntry.set(id, f as Record<string, unknown>);
  }
  for (const entries of Object.values(value.contributions))
    for (const c of Object.values(
      entries as Record<string, Record<string, unknown>>,
    )) {
      const f = byEntry.get(refusalIdentity(c));
      if (c.status === "refused") {
        if (
          !f ||
          f.required !== c.required ||
          (c.status_reason !== undefined && c.status_reason !== f.reason)
        )
          return "ErrInvalidContribution";
      } else if (f) return "ErrInvalidContribution";
    }
  return;
}
export interface AdmissionPolicy {
  kinds: Readonly<Record<string, KindDescriptor>>;
  regions: Readonly<Record<string, RegionDescriptor>>;
  reserved?: (kind: string, key: string) => boolean;
  validateMetadata?: (schema: unknown, metadata: unknown) => boolean;
}
export interface Plan {
  listed: RegistryContribution[];
  statusDiagnostics: Refusal[];
  accepted: RegistryContribution[];
  refusals: Refusal[];
  requiredFailed: boolean;
}
export function planResponse(
  r: PluginRegistryResponse,
  policy: AdmissionPolicy,
): Plan {
  const invalid = validateResponse(r);
  if (invalid) throw new RegistryError(invalid);
  const plan: Plan = {
    listed: [],
    statusDiagnostics: [],
    accepted: [],
    refusals: [...r.refusals],
    requiredFailed: r.refusals.some((f) => f.required),
  };
  for (const kind of Object.keys(r.contributions).sort())
    for (const key of Object.keys(r.contributions[kind]).sort()) {
      const c = r.contributions[kind][key],
        d = own(policy.kinds, kind),
        wire = own(r.kinds, kind);
      if (c.status === "refused") {
        plan.listed.push(c);
        continue;
      }
      let reason = "";
      if (
        c.owner_id === "core" ||
        policy.reserved?.(kind, key) ||
        (kind.startsWith("plugin.") &&
          !kind.startsWith(`plugin.${c.owner_id}.`))
      )
        reason = "reserved";
      else if (!d || !wire) reason = "unsupported-kind";
      else if (
        d.schema_version !== c.schema_version ||
        wire.schema_version !== c.schema_version
      )
        reason = "unsupported-schema";
      else if (
        !d.representations.includes(c.representation) ||
        !wire.representations.includes(c.representation)
      )
        reason = "unsupported-representation";
      if (!reason && c.component && d && wire) {
        const region = c.component.region,
          rd = own(policy.regions, region),
          wr = own(r.regions, region);
        if (
          !rd ||
          !wr ||
          !d.regions.includes(region) ||
          !wire.regions.includes(region) ||
          !rd.kinds.includes(kind) ||
          !wr.kinds.includes(kind) ||
          !rd.representations.includes("component") ||
          !wr.representations.includes("component")
        )
          reason = "unsupported-region";
      }
      if (!reason && d) {
        if (policy.validateMetadata) {
          if (!policy.validateMetadata(d.metadata_schema, c.metadata))
            reason = "invalid-metadata";
        } else if (
          !record(d.metadata_schema) ||
          Object.keys(d.metadata_schema).length
        )
          reason = "unsupported-metadata-schema";
      }
      if (reason) {
        plan.refusals.push({
          owner_id: c.owner_id,
          owner_generation: c.owner_generation,
          kind,
          local_key: c.local_key,
          required: c.required,
          reason,
        });
        plan.requiredFailed ||= c.required;
      } else {
        plan.listed.push(c);
        if (c.status === "accepted") plan.accepted.push(c);
        else if (!["declared_not_selected", "unavailable"].includes(c.status))
          plan.statusDiagnostics.push({
            owner_id: c.owner_id,
            owner_generation: c.owner_generation,
            kind,
            local_key: c.local_key,
            required: c.required,
            reason: "unknown-status",
          });
      }
    }
  return plan;
}

function collisions(value: unknown): boolean {
  if (value === null || typeof value !== "object") return false;
  if (!Array.isArray(value)) {
    const keys = Object.keys(value).map(foldKey);
    if (new Set(keys).size !== keys.length) return true;
  }
  return Object.values(value).some(collisions);
}
/** Structural validation is independent of host admission and never throws. */
export function validateResponse(value: unknown): string | undefined {
  try {
    if (collisions(value)) return "ErrCollision";
    return validate(value);
  } catch {
    return "ErrInvalidContribution";
  }
}
