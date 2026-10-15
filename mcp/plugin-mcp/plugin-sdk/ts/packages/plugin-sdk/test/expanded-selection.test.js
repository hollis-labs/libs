import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { selectExpandedCases } from "./expanded-selection.js";

const fixture = JSON.parse(await readFile(new URL("../../../../protocol/v2/fixtures/duplex-child.json", import.meta.url), "utf8"));
const clone = () => structuredClone(fixture);
const internal = "internal-test-only", negotiated = "normal-serve-negotiated";

// Reference selection at the original fixture pin: exclusion precedes status.
function originalSelection(source, mode, names) {
  return source.filter(recipe => (!names || names.includes(recipe.name)) &&
    !(mode === negotiated && recipe.scenario === "base-cancel"));
}

test("authored selection preserves old ordered recipes, metadata and both modes", () => {
  for (const mode of [internal, negotiated]) {
    assert.deepEqual(selectExpandedCases(fixture, mode), originalSelection(fixture.expanded, mode));
    const proposed = selectExpandedCases(fixture, mode).find(recipe => recipe.status === "proposed");
    assert.equal(proposed, fixture.expanded.find(recipe => recipe.status === "proposed"));
    assert.ok(proposed.owner.trim() && proposed.reason.trim());
  }
  assert.ok(selectExpandedCases(fixture, internal).some(recipe => recipe.scenario === "base-cancel"));
  assert.ok(!selectExpandedCases(fixture, negotiated).some(recipe => recipe.scenario === "base-cancel"));
});

test("source-group selection is generic and subset order follows source", () => {
  const m = clone();
  m.expanded_selection.source_group = "authored_group";
  m.authored_group = [
    {name:"first", scenario:"future-scenario", profile:"fixture", level:"normative"},
    {name:"second", status:"proposed", owner:"maintainers", reason:"pending design", level:"normative"},
  ];
  m.future_group = {pending:"consumer must report unknown groups separately"};
  assert.deepEqual(selectExpandedCases(m, internal, ["second", "first"]), m.authored_group);
  assert.equal(selectExpandedCases(m, internal)[0].status, undefined);
  // Selector retains the source status, never synthesizing a pass/unavailable result.
  assert.equal(selectExpandedCases(m, internal)[1].status, "proposed");
  assert.deepEqual(selectExpandedCases(m, internal, []), []);
  m.authored_group = [];
  assert.deepEqual(selectExpandedCases(m, negotiated), []);
  assert.throws(() => selectExpandedCases(m, undefined), /selection mode/);
  assert.throws(() => selectExpandedCases(m, "future-mode"), /selection mode/);
});

test("scenario exclusions apply before proposed disposition without skipping validation", () => {
  const m = clone();
  m.expanded.push({name:"proposed-base", scenario:"base-cancel", status:"proposed", owner:"maintainers", reason:"pending", level:"normative"});
  assert.ok(selectExpandedCases(m, internal).some(recipe => recipe.name === "proposed-base"));
  assert.ok(!selectExpandedCases(m, negotiated).some(recipe => recipe.name === "proposed-base"));
  m.expanded.at(-1).owner = " ";
  assert.throws(() => selectExpandedCases(m, negotiated, [m.expanded[0].name]), /proposed owner/);
});

test("unknown/excluded requested names and duplicate requests refuse instead of empty success", () => {
  for (const names of [["unknown-case"], [fixture.expanded[0].name, fixture.expanded[0].name], "all", [null]]) {
    assert.throws(() => selectExpandedCases(fixture, internal, names));
  }
  const excluded = fixture.expanded.find(recipe => recipe.scenario === "base-cancel").name;
  assert.throws(() => selectExpandedCases(fixture, negotiated, [excluded]), /excluded requested/);
});

test("unsupported or malformed selector and linkage metadata refuses", () => {
  const mutations = [
    m => {delete m.expanded_selection;},
    m => {m.expanded_selection = null;},
    m => {m.expanded_selection.version = 2;},
    m => {m.expanded_selection.version = "1";},
    m => {m.expanded_selection.kind = "all-cases";},
    m => {m.expanded_selection.source_group = "missing";},
    m => {m.expanded_selection.source_group = "toString";},
    m => {m.expanded_selection.source_group = [];},
    m => {m.expanded = {};},
    m => {m.expanded_selection.modes = [];},
    m => {delete m.expanded_selection.modes[negotiated];},
    m => {m.expanded_selection.modes.future = {exclude_scenarios:[]};},
    m => {m.expanded_selection.include_cases = [];},
    m => {m.expanded_selection.modes[internal].include_scenarios = [];},
    m => {m.expanded_selection.modes[internal].exclude_scenarios = "base-cancel";},
    m => {m.expanded_selection.modes[internal].exclude_scenarios = [null];},
    m => {m.expanded_selection.modes[internal].exclude_scenarios = ["base-cancel", "base-cancel"];},
    m => {delete m.negotiated.expanded_selection;},
    m => {m.negotiated.expanded_selection = internal;},
  ];
  for (const mutate of mutations) {
    const m = clone(); mutate(m);
    assert.throws(() => selectExpandedCases(m, internal), mutate.toString());
  }
});

test("full group validates before selection, even late, excluded or unrequested rows", () => {
  const mutations = [
    m => {m.expanded.push(null);},
    m => {m.expanded.at(-1).status = "passed";},
    m => {m.expanded.at(-1).status = null;},
    m => {m.expanded.at(-1).owner = 1;},
    m => {delete m.expanded.at(-1).reason;},
    m => {m.expanded.at(-1).scenario = [];},
    m => {m.expanded[2].status = "unavailable";},
    m => {m.expanded[2].profile = null;},
    m => {m.expanded[2].scenario = "";},
    m => {m.expanded[2].level = "observed-quirk";},
    m => {m.expanded.at(-1).name = m.expanded[0].name;},
  ];
  for (const mutate of mutations) {
    const m = clone(); mutate(m);
    assert.throws(() => selectExpandedCases(m, negotiated, [m.expanded[0].name]), mutate.toString());
  }
});
