import { createHash } from "node:crypto";
import { createPluginRegistry } from "../dist/index.js";
export const source =
  "export const Panel = () => null; export const Extra = () => null;";
export const bytes = new TextEncoder().encode(source);
export const digest = (value) =>
  `sha256:${createHash("sha256").update(value).digest("hex")}`;
export const panel = {
  schema_version: 1,
  metadata_schema: {},
  representations: ["component"],
  regions: ["rail"],
  required_capabilities: [],
};
export const nav = {
  schema_version: 1,
  metadata_schema: {},
  representations: ["declarative"],
  regions: [],
  required_capabilities: [],
};
export const handler = {
  schema_version: 1,
  metadata_schema: {},
  representations: ["handler"],
  regions: [],
  required_capabilities: [],
};
export const rail = {
  kinds: ["panel"],
  representations: ["component"],
  context_schema: {},
  ordering: "priority-ascending",
};
export const policy = {
  kinds: { panel, "nav.item": nav, "mcp.tool": handler },
  regions: { rail },
};
export function entry({
  owner = "p",
  generation = "1",
  local = "main",
  kind = "panel",
  required = true,
  representation = "component",
  metadata = {},
  ...fields
} = {}) {
  return {
    owner_id: owner,
    owner_generation: generation,
    local_key: local,
    kind,
    schema_version: 1,
    required,
    status: "accepted",
    representation,
    metadata,
    ...(representation === "component"
      ? { component: { export: "Panel", region: "rail" } }
      : representation === "declarative"
        ? { declarative: { label: "Notes" } }
        : { handler: { id: "notes.read" } }),
    ...fields,
  };
}
export function response(revision = 1, generation = "1") {
  return {
    registry_version: 2,
    host_instance: "epoch",
    revision,
    plugins: {
      p: {
        owner_generation: generation,
        bundle_url: "/p.js",
        bundle_version: digest(bytes),
        runtime: [{ name: "react", min: "19.0.0", max: "19.9.9" }],
      },
    },
    kinds: structuredClone(policy.kinds),
    regions: structuredClone(policy.regions),
    contributions: { panel: { "p/main": entry({ generation }) } },
    refusals: [],
  };
}
export function registry(options = {}) {
  return createPluginRegistry({
    ...policy,
    runtimes: { react: "19.1.0" },
    stylesheets: false,
    fetchBundle: async () => bytes,
    importModule: async () => ({ Panel: () => null, Extra: () => null }),
    ...options,
  });
}
export function deferred() {
  let resolve, reject;
  const promise = new Promise((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}
