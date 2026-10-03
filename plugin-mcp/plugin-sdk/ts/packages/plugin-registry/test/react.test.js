import { test } from "node:test";
import assert from "node:assert/strict";
import { createReactPluginRegistry, reactAdopt } from "../dist/react.js";
import { policy, response, bytes, entry } from "./helpers.js";
const options = {
  ...policy,
  runtimes: { react: "19.1.0" },
  stylesheets: false,
  fetchBundle: async () => bytes,
  importModule: async () => ({ Panel: () => null }),
};
async function lazyComponent(lazy) {
  let pending;
  try {
    lazy._init(lazy._payload);
  } catch (value) {
    pending = value;
  }
  if (pending && typeof pending.then === "function") await pending;
  return lazy._init(lazy._payload);
}

test("reactAdopt produces a lazy type with a live generation fence", async () => {
  let active = true;
  const Component = () => null;
  const adopted = reactAdopt({
    ...entry(),
    hostInstance: "epoch",
    key: "p/main",
    pluginId: "p",
    exportName: "Panel",
    export: Component,
    isActive: () => active,
  });
  assert.equal(adopted.$$typeof, Symbol.for("react.lazy"));
  const Guard = await lazyComponent(adopted);
  assert.equal(Guard({}).type, Component);
  active = false;
  assert.equal(Guard({}), null);
});
test("React adapter adopts only declared components and respects host overrides", async () => {
  const registry = createReactPluginRegistry(options);
  await registry.sync(response());
  assert.equal(
    registry.get("panel", "p/main").value.$$typeof,
    Symbol.for("react.lazy"),
  );
  const custom = createReactPluginRegistry({
    ...options,
    adopt: (r) => ({ contained: r.exportName }),
  });
  await custom.sync(response());
  assert.deepEqual(custom.get("panel", "p/main").value, { contained: "Panel" });
});
test("captured React component returns no old component after reload/unload", async () => {
  const registry = createReactPluginRegistry(options);
  await registry.sync(response());
  const Guard = await lazyComponent(registry.get("panel", "p/main").value);
  assert.ok(Guard({}));
  await registry.sync(response(2, "2"));
  assert.equal(Guard({}), null);
  const NewGuard = await lazyComponent(registry.get("panel", "p/main").value);
  assert.ok(NewGuard({}));
  await registry.unload("p");
  assert.equal(NewGuard({}), null);
});
test("declarative regions do not become arbitrary lazy components", async () => {
  const registry = createReactPluginRegistry(options);
  const c = response();
  c.contributions = {
    "nav.item": {
      "p/nav": entry({
        local: "nav",
        kind: "nav.item",
        representation: "declarative",
      }),
    },
  };
  await registry.sync(c);
  assert.equal(registry.get("nav.item", "p/nav").value.label, "Notes");
  assert.equal(registry.get("nav.item", "p/nav").value.$$typeof, undefined);
});
test("React adapter retains core admission opt-in and named reservation", async () => {
  const registry = createReactPluginRegistry({
    ...options,
    reserved: () => true,
  });
  assert.equal((await registry.sync(response())).accepted, false);
});

test("React never resolves inactive statuses and fences a newly unselected captured component", async () => {
  for (const status of [
    "declared_not_selected",
    "unavailable",
    "future_status",
  ]) {
    const registry = createReactPluginRegistry({
      ...options,
      fetchBundle: async () => {
        assert.fail("inactive React bundle fetched");
      },
      importModule: async () => {
        assert.fail("inactive React bundle imported");
      },
    });
    const c = response();
    c.contributions.panel["p/main"].status = status;
    assert.equal((await registry.sync(c)).accepted, true);
    assert.equal(registry.get("panel", "p/main"), undefined);
    assert.equal(registry.snapshot().contributions[0].status, status);
  }
  const registry = createReactPluginRegistry(options);
  await registry.sync(response());
  const Guard = await lazyComponent(registry.get("panel", "p/main").value);
  const c = response(2);
  c.contributions.panel["p/main"].status = "declared_not_selected";
  await registry.sync(c);
  assert.equal(Guard({}), null);
});
