import { test } from "node:test";
import assert from "node:assert/strict";
import {
  REGISTRY_VERSION,
  parseRegistryResponse,
  RegistryError,
  validateResponse,
  planResponse,
  checkRuntimes,
} from "../dist/index.js";
import { response } from "./helpers.js";

test("registry version is locked at 2; legacy and dual keys have no fallback", () => {
  assert.equal(REGISTRY_VERSION, 2);
  const legacy = response();
  delete legacy.registry_version;
  legacy.protocol = 1;
  for (const value of [
    legacy,
    { ...response(), protocol: 2 },
    { ...response(), Protocol: 2 },
  ])
    assert.equal(validateResponse(value), "ErrRegistryVersion");
  assert.equal(
    validateResponse({ ...response(), registry_version: 1 }),
    "ErrRegistryVersion",
  );
});
test("raw parser rejects exact, escaped and case-variant duplicate keys at every depth", () => {
  for (const raw of [
    '{"revision":1,"revision":2}',
    '{"required":true,"REQUIRED":false}',
    '{"x":{"a":1,"\\u0061":2}}',
    '{"metadata":{"key":1,"KEY":2}}',
    '{"metadata":{"ΟΣ":1,"οσ":2}}',
    '{"metadata":{"İ":1,"i":2}}',
  ])
    assert.throws(
      () => parseRegistryResponse(raw),
      (err) => err instanceof RegistryError && err.code === "ErrCollision",
    );
});
test("raw parser preserves escaped strings and nested arrays", () => {
  const c = response();
  c.contributions.panel["p/main"].metadata = {
    value: 'quote: " and escape \\',
    array: [null, { nested: "a" }],
  };
  assert.deepEqual(parseRegistryResponse(JSON.stringify(c)), c);
});
test("raw parser fails invalid JSON and malformed input with typed errors", () => {
  for (const raw of ["{", '{"a":1,}', "true", "{}", '{"a":1} garbage'])
    assert.throws(() => parseRegistryResponse(raw), RegistryError);
});
test("mis-cased known fields are rejected rather than treated as unknown extensions", () => {
  for (const mutate of [
    (r) => {
      r.reviſion = 1;
    },
    (r) => {
      r.Revision = r.revision;
      delete r.revision;
    },
    (r) => {
      r.plugins.p.Bundle_URL = r.plugins.p.bundle_url;
      delete r.plugins.p.bundle_url;
    },
    (r) => {
      r.contributions.panel["p/main"].Required = true;
      delete r.contributions.panel["p/main"].required;
    },
  ]) {
    const c = response();
    mutate(c);
    assert.equal(validateResponse(c), "ErrInvalidContribution");
  }
});
test("missing/null mandatory booleans and forbidden null representation fields fail", () => {
  for (const mutate of [
    (r) => delete r.contributions.panel["p/main"].required,
    (r) => (r.contributions.panel["p/main"].required = null),
    (r) => (r.contributions.panel["p/main"].declarative = null),
    (r) =>
      r.refusals.push({
        owner_id: "p",
        owner_generation: "1",
        kind: "panel",
        local_key: "x",
        reason: "unsupported-kind",
      }),
  ]) {
    const c = response();
    mutate(c);
    assert.equal(validateResponse(c), "ErrInvalidContribution");
  }
});
test("structural validation refuses inherited and non-JSON values", () => {
  const inherited = Object.create(response());
  assert.equal(validateResponse(inherited), "ErrRegistryVersion");
  const c = response();
  c.plugins = Object.create(c.plugins);
  assert.equal(validateResponse(c), "ErrInvalidContribution");
  for (const value of [undefined, Infinity, new Date(), () => null]) {
    const c = response();
    c.contributions.panel["p/main"].metadata = value;
    assert.equal(validateResponse(c), "ErrInvalidContribution");
  }
  const cycle = {};
  cycle.self = cycle;
  const cyc = response();
  cyc.contributions.panel["p/main"].metadata = cycle;
  assert.equal(validateResponse(cyc), "ErrInvalidContribution");
});
test("inherited owner records do not satisfy an explicit declaration", () => {
  const c = response();
  delete c.plugins.p;
  c.contributions.panel = {
    "constructor/main": {
      ...c.contributions.panel["p/main"],
      owner_id: "constructor",
    },
  };
  assert.equal(validateResponse(c), "ErrUnknownPlugin");
});
test("unsafe revisions, non-digests and public binding collisions fail", () => {
  for (const revision of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])
    assert.equal(
      validateResponse({ ...response(), revision }),
      "ErrInvalidContribution",
    );
  const c = response();
  c.plugins.p.bundle_version = "mtime";
  assert.equal(validateResponse(c), "ErrIntegrity");
  const dupe = response();
  dupe.contributions.panel["p/main"].public_binding = "same";
  dupe.contributions.panel["p/other"] = {
    ...dupe.contributions.panel["p/main"],
    local_key: "other",
  };
  assert.equal(validateResponse(dupe), "ErrCollision");
});
test("normalized runtime bounds use inclusive numeric semantic ordering", () => {
  assert.equal(
    checkRuntimes([{ name: "react", min: "19.2.0", max: "19.10.0" }], {
      react: "19.10.0",
    }),
    true,
  );
  assert.equal(
    checkRuntimes([{ name: "react", min: "19.10.0" }], { react: "19.2.0" }),
    false,
  );
  for (const versions of [{}, { react: "^19.0.0" }, { react: "19.0.0-beta.1" }])
    assert.equal(
      checkRuntimes([{ name: "react", min: "19.0.0" }], versions),
      false,
    );
  assert.equal(
    checkRuntimes(
      [{ name: "react", min: "19.0.0-beta.1", max: "19.0.0" }],
      { react: "19.0.0-beta.2" },
      true,
    ),
    true,
  );
  assert.equal(
    checkRuntimes(
      [{ name: "react", min: "19.0.0-beta.2" }],
      { react: "19.0.0-beta.10" },
      true,
    ),
    true,
  );
  assert.equal(
    checkRuntimes(
      [{ name: "react", min: "19.0.0" }],
      Object.create({ react: "19.0.0" }),
    ),
    false,
  );
});

test("public planning validates before host callbacks or admission", () => {
  const c = response();
  delete c.contributions.panel["p/main"].status;
  assert.throws(
    () =>
      planResponse(c, {
        kinds: {},
        regions: {},
        reserved: () => assert.fail("policy ran before validation"),
      }),
    (error) =>
      error instanceof RegistryError && error.code === "ErrInvalidContribution",
  );
});

test("integer spelling checks use decoded wire paths and preserve duplicate precedence", () => {
  const raw = JSON.stringify(response()).replace(
    '"revision":1',
    '"revi\\u0073ion":5e0',
  );
  assert.throws(
    () => parseRegistryResponse(raw),
    (e) => e instanceof RegistryError && e.code === "ErrInvalidContribution",
  );
  assert.throws(
    () => parseRegistryResponse('{"revision":5.0,"REVISION":6}'),
    (e) => e instanceof RegistryError && e.code === "ErrCollision",
  );
});
