// Standalone test metadata only. No runtime result changes authored status.
import { Buffer } from "node:buffer";
import { decodeJSONObject, validateJSON } from "../dist/strict-json.js";

const LINKS = Object.freeze({
  "public-conn-isolated-transport": "lifecycle_compatible_v2",
  "sdk-normal-serve-remote-overflow": "sdk_remote_overflow_v2",
});
const IDENTIFIER = /^[a-z][a-z0-9_-]*$/;
export const LIFECYCLE_MANIFEST_BYTES = 8 * 1024 * 1024;
function requireValue(ok, message) {
  if (!ok) throw new TypeError("lifecycle selection: " + message);
}
function identifier(value) {
  requireValue(typeof value === "string" && IDENTIFIER.exec(value)?.[0] === value, "invalid identifier");
  return value;
}
function nonblank(value) {
  requireValue(typeof value === "string" && /[^ \t\r\n]/.test(value), "nonblank text required");
}
function object(raw, fields) {
  return decodeJSONObject(raw, fields);
}
function json(raw) { return JSON.parse(raw); }
function identifiers(value) {
  requireValue(Array.isArray(value), "identifier array required");
  const seen = new Set();
  for (const entry of value) {
    identifier(entry);
    requireValue(!seen.has(entry), "duplicate identifier");
    seen.add(entry);
  }
  return seen;
}
function textInput(raw) {
  requireValue(typeof raw === "string" || raw instanceof Uint8Array, "raw UTF-8 input required");
  if (typeof raw === "string") {
    // Encoding a lone surrogate would replace it before the strict JSON check.
    requireValue(raw.isWellFormed(), "invalid Unicode input");
  }
  const bytes = typeof raw === "string" ? Buffer.from(raw, "utf8") : raw;
  requireValue(bytes.byteLength <= LIFECYCLE_MANIFEST_BYTES, "manifest byte limit");
  requireValue(!(bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf), "BOM refused");
  const text = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  validateJSON(text); // All nested duplicate decoded keys and depth before JSON.parse.
  return text;
}

export function selectLifecycleCases(raw, mode, names = undefined) {
  const top = object(textInput(raw), ["corpus_version", "contract_version", "selection", "groups"]);
  requireValue(top.get("corpus_version") === "1" && top.get("contract_version") === "2", "canonical versions required");
  const selection = object(top.get("selection"), ["version", "kind", "modes"]);
  requireValue(selection.get("version") === "1" && json(selection.get("kind")) === "source-group", "unsupported version/operator");
  const groupsRaw = object(top.get("groups"), Object.values(LINKS));
  const modesRaw = object(selection.get("modes"), Object.keys(LINKS));
  const groups = new Map(), globalNames = new Set();
  // Every group is validated before either mode or a subset is considered.
  for (const group of Object.values(LINKS)) {
    const rows = json(groupsRaw.get(group));
    requireValue(Array.isArray(rows) && rows.length > 0, "nonempty source array required");
    const scenarios = new Set();
    for (const row of rows) {
      // Input-wide duplicate validation already ran before this re-encoding.
      object(JSON.stringify(row), ["name", "scenario", "profile", "level", "status", "owner", "reason"]);
      identifier(row.name); identifier(row.scenario); identifier(row.profile);
      requireValue(row.level === "normative", "unsupported level");
      requireValue(row.status === "proposed" || row.status === "observed", "explicit status required");
      nonblank(row.owner); nonblank(row.reason);
      requireValue(!globalNames.has(row.name), "duplicate global case name");
      requireValue(!scenarios.has(row.scenario), "duplicate group scenario");
      globalNames.add(row.name); scenarios.add(row.scenario);
    }
    groups.set(group, { rows, scenarios });
  }
  const modes = new Map();
  for (const [name, expectedGroup] of Object.entries(LINKS)) {
    const link = object(modesRaw.get(name), ["source_group", "exclude_scenarios"]);
    requireValue(json(link.get("source_group")) === expectedGroup, "unknown/cross-mode source link");
    const excluded = identifiers(json(link.get("exclude_scenarios")));
    for (const scenario of excluded)
      requireValue(groups.get(expectedGroup).scenarios.has(scenario), "unknown/foreign exclusion");
    modes.set(name, { group: expectedGroup, excluded });
  }
  requireValue(typeof mode === "string" && modes.has(mode), "unknown/missing mode");
  const linked = modes.get(mode);
  const selected = groups.get(linked.group).rows.filter(row => !linked.excluded.has(row.scenario));
  if (names === undefined) return selected;
  const requested = identifiers(names), available = new Set(selected.map(row => row.name));
  for (const name of requested)
    requireValue(available.has(name), "unknown/excluded/foreign requested name");
  return selected.filter(row => requested.has(row.name));
}
