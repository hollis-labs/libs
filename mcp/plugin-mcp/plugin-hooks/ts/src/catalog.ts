import { strictDocument, validPointer, parse, numericLiteral } from "./json.js";
export type Kind = "action" | "filter";
export type Mode =
  "sequential" | "parallel" | "bail" | "waterfall" | "async" | "after_commit";
export type ErrorPolicy = "open" | "closed";
export interface Deprecation {
  since: string;
  replacement?: string;
  reason: string;
  removal: string;
}
export interface Definition {
  name: string;
  owner_namespace?: string;
  kind: Kind;
  mode: Mode;
  input_schema: Record<string, unknown>;
  output_schema?: Record<string, unknown>;
  mutable_paths: string[];
  since: string;
  deprecated?: Deprecation;
  remote_ok: boolean;
  budget_ms: number;
  handler_timeout_ms: number;
  on_error_default: ErrorPolicy;
  allowed_on_error: ErrorPolicy[];
  max_payload_bytes: number;
  max_handlers: number;
  max_parallelism: number;
  views: Record<string, string[]>;
  required_view?: string;
  schema_digest: string;
}
export interface CatalogDocument {
  catalog_version: string;
  definitions: Definition[];
}
export type Validator = (json: string) => void;
export interface Validators {
  input: Validator;
  output?: Validator;
}
export interface SchemaDocuments {
  input: string;
  output?: string;
}
export type CompileValidators = (
  definition: Readonly<Definition>,
  schemas: Readonly<SchemaDocuments>,
) => Validators;
export class HookError extends Error {
  constructor(
    readonly code: string,
    message = code,
  ) {
    super(message);
    this.name = "HookError";
  }
}
export const ErrCancelled = new HookError("cancelled");
export const ErrApprovalRequired = new HookError("approval_required");
export function fail(code: string, message?: string): never {
  throw new HookError(code, message);
}
const namePattern = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/;
const record = (v: unknown): v is Record<string, unknown> =>
  v !== null && typeof v === "object" && !Array.isArray(v);
const text = (v: unknown): v is string => typeof v === "string" && v.length > 0;
const positive = (v: unknown): v is number =>
  typeof v === "number" && Number.isFinite(v) && v > 0;
function keys(
  o: Record<string, unknown>,
  allowed: string[],
  required: string[],
): void {
  if (
    Object.keys(o).some((k) => !allowed.includes(k)) ||
    required.some((k) => !Object.hasOwn(o, k))
  )
    fail("invalid_definition");
}
function pointers(v: unknown): v is string[] {
  return (
    Array.isArray(v) && v.every((p) => typeof p === "string" && validPointer(p))
  );
}
export function readCatalog(json: string): CatalogDocument {
  let c: unknown;
  try {
    c = strictDocument(json);
  } catch {
    return fail("invalid_definition");
  }
  if (!record(c)) return fail("invalid_definition");
  keys(
    c,
    ["catalog_version", "definitions"],
    ["catalog_version", "definitions"],
  );
  if (!text(c.catalog_version) || !Array.isArray(c.definitions))
    return fail("invalid_definition");
  const rawDefinitions = (
    parse(json) as {
      definitions: { [key: string]: import("./json.js").Json }[];
    }
  ).definitions;
  let definitionIndex = 0;
  const seen = new Set<string>();
  for (const d of c.definitions as unknown[]) {
    if (!record(d)) return fail("invalid_definition");
    const rawDefinition = rawDefinitions[definitionIndex++]!;
    const required = [
      "name",
      "kind",
      "mode",
      "input_schema",
      "mutable_paths",
      "since",
      "remote_ok",
      "budget_ms",
      "handler_timeout_ms",
      "on_error_default",
      "allowed_on_error",
      "max_payload_bytes",
      "max_handlers",
      "max_parallelism",
      "views",
      "schema_digest",
    ];
    keys(
      d,
      [
        ...required,
        "owner_namespace",
        "output_schema",
        "deprecated",
        "required_view",
      ],
      required,
    );
    if (
      !text(d.name) ||
      !namePattern.test(d.name) ||
      !text(d.since) ||
      !text(d.schema_digest) ||
      !record(d.input_schema)
    )
      return fail("invalid_definition");
    if (seen.has(d.name)) return fail("duplicate");
    seen.add(d.name);
    const ns = d.owner_namespace;
    const parts = d.name.split(".");
    if (ns !== undefined && !text(ns)) return fail("invalid_definition");
    if (
      parts[0] === "plugin"
        ? parts.length !== 4 || !ns || parts[1] !== ns
        : ns !== undefined
    )
      return fail("invalid_definition");
    if (
      typeof d.remote_ok !== "boolean" ||
      !positive(d.budget_ms) ||
      !positive(d.handler_timeout_ms) ||
      d.handler_timeout_ms > d.budget_ms
    )
      return fail("invalid_definition");
    for (const k of ["max_payload_bytes", "max_handlers", "max_parallelism"])
      if (
        !positive(d[k]) ||
        !Number.isSafeInteger(d[k]) ||
        !/^[0-9]+$/.test(numericLiteral(rawDefinition[k]!) ?? "")
      )
        return fail("invalid_definition");
    if (
      !["open", "closed"].includes(String(d.on_error_default)) ||
      !Array.isArray(d.allowed_on_error) ||
      !d.allowed_on_error.includes(d.on_error_default) ||
      d.allowed_on_error.some((p) => p !== "open" && p !== "closed")
    )
      return fail("invalid_definition");
    if (
      !pointers(d.mutable_paths) ||
      !record(d.views) ||
      Object.entries(d.views).some(([k, v]) => !k || !pointers(v))
    )
      return fail("invalid_definition");
    if (
      d.required_view !== undefined &&
      (!text(d.required_view) || !Object.hasOwn(d.views, d.required_view))
    )
      return fail("invalid_definition");
    if (typeof d.mode !== "string") return fail("invalid_definition");
    if (d.kind === "action") {
      if (
        !["sequential", "parallel", "bail", "async", "after_commit"].includes(
          String(d.mode),
        ) ||
        Object.hasOwn(d, "output_schema") ||
        d.mutable_paths.length
      )
        return fail("invalid_definition");
    } else if (
      d.kind !== "filter" ||
      d.mode !== "waterfall" ||
      !record(d.output_schema)
    )
      return fail("invalid_definition");
    if (d.deprecated !== undefined) {
      if (!record(d.deprecated)) return fail("invalid_definition");
      const p = d.deprecated;
      keys(
        p,
        ["since", "replacement", "reason", "removal"],
        ["since", "reason", "removal"],
      );
      if (
        !text(p.since) ||
        !text(p.reason) ||
        !text(p.removal) ||
        (p.replacement !== undefined &&
          (!text(p.replacement) ||
            !namePattern.test(p.replacement) ||
            p.replacement === d.name))
      )
        return fail("invalid_definition");
    }
  }
  return c as unknown as CatalogDocument;
}
