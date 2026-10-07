// Test-only declarative fixture selection; no runtime or host policy is inferred.
import assert from "node:assert/strict";

function object(value, fields, label) {
  assert.ok(value !== null && typeof value === "object" && !Array.isArray(value), `${label} must be an object`);
  assert.deepEqual(Object.keys(value).sort(), [...fields].sort(), `${label} has missing or unsupported fields`);
}
function text(value, label) {
  assert.ok(typeof value === "string" && value.trim().length > 0, `${label} must be a nonblank string`);
}
function strings(value, label) {
  assert.ok(Array.isArray(value), `${label} must be an array`);
  for (const entry of value) text(entry, label);
  assert.equal(new Set(value).size, value.length, `${label} contains duplicates`);
}

export function selectExpandedCases(manifest, mode, names) {
  const selector = manifest.expanded_selection;
  object(selector, ["version", "kind", "source_group", "modes"], "expanded_selection");
  assert.equal(selector.version, 1, "unsupported expanded selection version");
  assert.equal(selector.kind, "source-group", "unsupported expanded selection kind");
  text(selector.source_group, "source_group");
  assert.ok(Object.hasOwn(manifest, selector.source_group), "missing referenced source group");
  const source = manifest[selector.source_group];
  assert.ok(Array.isArray(source), "referenced source group must be an array");
  object(selector.modes, ["internal-test-only", "normal-serve-negotiated"], "selection modes");
  for (const [name, selection] of Object.entries(selector.modes)) {
    object(selection, ["exclude_scenarios"], `mode ${name}`);
    strings(selection.exclude_scenarios, `mode ${name} exclusions`);
  }
  assert.equal(manifest.negotiated?.expanded_selection, "normal-serve-negotiated", "missing or unsupported negotiated selection linkage");
  assert.ok(typeof mode === "string" && Object.hasOwn(selector.modes, mode), "missing or unsupported selection mode");

  // All rows are validated, including exclusions and rows outside a CLI subset.
  const identities = new Set();
  for (const recipe of source) {
    assert.ok(recipe !== null && typeof recipe === "object" && !Array.isArray(recipe), "source recipe must be an object");
    text(recipe.name, "case name");
    assert.ok(!identities.has(recipe.name), "duplicate source case name");
    identities.add(recipe.name);
    assert.equal(recipe.level, "normative", "unsupported source level");
    // Only absence defaults to observed; null and other statuses are refused.
    const status = Object.hasOwn(recipe, "status") ? recipe.status : "observed";
    assert.ok(status === "observed" || status === "proposed", "unsupported source status");
    if (status === "observed") {
      text(recipe.scenario, "observed scenario");
      text(recipe.profile, "observed profile");
    } else {
      text(recipe.owner, "proposed owner");
      text(recipe.reason, "proposed reason");
      if (Object.hasOwn(recipe, "scenario")) text(recipe.scenario, "proposed scenario");
      if (Object.hasOwn(recipe, "profile")) text(recipe.profile, "proposed profile");
    }
  }
  const exclusions = selector.modes[mode].exclude_scenarios;
  // Exclusion applies before disposition, including future proposed rows.
  const selected = source.filter(recipe => !exclusions.includes(recipe.scenario));
  if (names === undefined) return selected;
  strings(names, "requested cases");
  for (const name of names) {
    assert.ok(selected.some(recipe => recipe.name === name), `unknown or excluded requested case: ${name}`);
  }
  // A subset retains source order, independent of the requested-name order.
  return selected.filter(recipe => names.includes(recipe.name));
}
