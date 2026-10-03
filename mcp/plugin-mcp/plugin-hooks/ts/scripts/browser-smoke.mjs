// Execute built ESM in a realm with browser globals and no Node core globals.
// File loading and module linking belong only to this development script.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { SourceTextModule, createContext } from "node:vm";
const { catalog } = JSON.parse(
  await readFile(
    new URL("../../conformance/execution.json", import.meta.url),
    "utf8",
  ),
);
const realm = createContext({
  AbortController,
  AbortSignal,
  TextEncoder,
  structuredClone,
  performance,
  setTimeout,
  clearTimeout,
  queueMicrotask,
});
const modules = new Map();
function load(url) {
  if (!modules.has(url.href)) {
    modules.set(
      url.href,
      readFile(url, "utf8").then(
        (source) =>
          new SourceTextModule(source, {
            context: realm,
            identifier: url.href,
          }),
      ),
    );
  }
  return modules.get(url.href);
}
const entry = await load(new URL("../dist/index.js", import.meta.url));
await entry.link((specifier, referencing) =>
  load(new URL(specifier, referencing.identifier)),
);
await entry.evaluate();
const { Registry, Engine } = entry.namespace;
const registry = new Registry(JSON.stringify(catalog), (d) => ({
  input: () => {},
  ...(d.kind === "filter" ? { output: () => {} } : {}),
}));
const engine = new Engine(registry);
const scope = registry.newScope({
  owner: "browser",
  generation: "1",
  hooks: ["value.change"],
});
scope.addFilter("value.change", "mask", { view: "masked" }, (i) => {
  assert.equal(i.payload, '{"value":1}');
  return '{"value":2}';
});
const result = await engine.applyFilters(
  "value.change",
  '{"value":1,"secret":9007199254740993}',
);
assert.equal(result.status, "success");
assert.equal(result.value, '{"secret":9007199254740993,"value":2}');
await scope.dispose();
await engine.shutdown();
console.log(
  "Built browser ESM: masked waterfall and lossless merge passed without Node core globals.",
);
