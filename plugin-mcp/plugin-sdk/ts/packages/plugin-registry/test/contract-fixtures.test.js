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
