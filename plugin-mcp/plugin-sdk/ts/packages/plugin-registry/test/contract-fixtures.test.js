import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import {
  REGISTRY_VERSION,
  parseRegistryResponse,
  validateResponse,
  RegistryError,
  planResponse,
  createPluginRegistry,
} from "../dist/index.js";

// Shared fixtures stay frozen per released registry version; both authored views use
// exactly these structural expectations, independently from host policy.
const dir = fileURLToPath(
  new URL(
    `../../../../registry/testdata/contract/registry-v${REGISTRY_VERSION}/`,
    import.meta.url,
  ),
);
const files = readdirSync(dir)
  .filter((f) => f.endsWith(".json"))
  .sort();
test("contract fixtures exist for the current registry version", () =>
  assert.notEqual(files.length, 0));
for (const file of files) {
  const fixture = JSON.parse(readFileSync(join(dir, file), "utf8"));
  test(`registry v2 fixture ${file}: ${fixture.description}`, () => {
    if (!fixture.response_raw)
      assert.equal(
        validateResponse(fixture.response) ?? "ok",
        fixture.ts.validate,
      );
    if (fixture.ts.validate === "ok")
      assert.deepEqual(
        parseRegistryResponse(
          fixture.response_raw ?? JSON.stringify(fixture.response),
        ),
        fixture.response_raw
          ? JSON.parse(fixture.response_raw)
          : fixture.response,
      );
    else
      assert.throws(
        () =>
          parseRegistryResponse(
            fixture.response_raw ?? JSON.stringify(fixture.response),
          ),
        (err) =>
          err instanceof RegistryError && err.code === fixture.ts.validate,
      );
  });
}
test("unknown optional shared fixture degrades visibly at admission", () => {
  const { response } = JSON.parse(
    readFileSync(join(dir, "unknown-optional-kind.json"), "utf8"),
  );
  const plan = planResponse(response, { kinds: {}, regions: {} });
  assert.equal(plan.requiredFailed, false);
  assert.equal(plan.accepted.length, 0);
  assert.equal(plan.refusals[0].reason, "unsupported-kind");
});

test("shared unknown-status projection normalizes plan and published snapshot", async () => {
  const fixture = JSON.parse(
    readFileSync(join(dir, "unknown-status.json"), "utf8"),
  );
  const response = fixture.response,
    expected = fixture.projection;
  const policy = { kinds: response.kinds, regions: response.regions };
  const plan = planResponse(response, policy);
  assert.equal(plan.listed[0].status, expected.status);
  assert.equal(plan.listed[0].status_reason, expected.status_reason);
  assert.equal(plan.accepted.length, expected.accepted);
  assert.deepEqual(
    plan.statusDiagnostics.map((d) => d.reason),
    expected.diagnostics,
  );
  const events = [];
  const registry = createPluginRegistry({
    ...policy,
    runtimes: { react: "19.1.0" },
    stylesheets: false,
    fetchBundle: async () => assert.fail("inactive bundle fetched"),
    importModule: async () => assert.fail("inactive bundle imported"),
    onDiagnostic: (event) => events.push(event),
  });
  assert.equal((await registry.sync(JSON.stringify(response))).accepted, true);
  const snapshot = registry.snapshot();
  assert.equal(snapshot.contributions[0].status, expected.status);
  assert.equal(snapshot.contributions[0].status_reason, expected.status_reason);
  assert.equal(snapshot.contributions[0].resolved, false);
  assert.deepEqual(
    events.map((event) => event.diagnostic.reason),
    expected.diagnostics,
  );
  const raw = response.contributions[expected.kind][expected.key].status;
  assert.equal(JSON.stringify({ plan, snapshot, events }).includes(raw), false);
});
