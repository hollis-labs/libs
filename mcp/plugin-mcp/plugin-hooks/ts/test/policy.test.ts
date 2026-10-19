import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { readCatalog, Registry, Engine } from "../src/index.js";
import type { CatalogDocument, Kind, Options } from "../src/index.js";
import { corpus, registry } from "./fixtures.js";
const cases = JSON.parse(
  readFileSync(
    new URL("../../conformance/policy.json", import.meta.url),
    "utf8",
  ),
) as {
  name: string;
  operation: string;
  error: string;
  hooks?: string[];
  owner?: string;
  generation?: string;
  remote?: boolean;
  hook?: string;
  registration?: string;
  kind?: Kind;
  patch?: Record<string, unknown>;
  options?: Options;
}[];
describe("shared registration policy", () => {
  for (const tc of cases)
    it(tc.name, () => {
      expect(() => {
        const doc = structuredClone(corpus.catalog) as CatalogDocument;
        if (tc.patch) Object.assign(doc.definitions[0]!, tc.patch);
        const r = new Registry(JSON.stringify(doc), (d) => ({
          input: () => {},
          ...(d.kind === "filter" ? { output: () => {} } : {}),
        }));
        if (tc.operation === "definition") return;
        const config = {
          owner: tc.owner ?? "example",
          generation: tc.generation ?? "1",
          hooks: tc.hooks ?? ["event.observe"],
          remote: tc.remote ?? false,
        };
        const s = r.newScope(config);
        if (tc.operation === "scope") return;
        if (tc.operation === "generation") {
          r.newScope(config);
          return;
        }
        const name = tc.registration ?? "handler";
        const hook = tc.hook ?? "event.observe";
        s.validateRegistration(
          hook,
          name,
          tc.kind ?? "action",
          tc.options ?? {},
        );
        if (
          tc.operation === "duplicate" ||
          tc.operation === "inprocess_registration"
        ) {
          s.addAction(hook, name, tc.options ?? {}, () => {});
          if (tc.operation === "duplicate")
            s.addAction(hook, name, tc.options ?? {}, () => {});
        }
      }).toThrowError(expect.objectContaining({ code: tc.error }));
    });
});
describe("raw catalog strictness", () => {
  const json = JSON.stringify(corpus.catalog);
  it.each([
    json.replace('"catalog_version":', '"Catalog_version":'),
    json.replace('"catalog_version":', '"extra":true,"catalog_version":'),
    json.replace(
      '"catalog_version":',
      '"catalog_version":"duplicate","catalog_version":',
    ),
    json.replace('"max_handlers":16', '"max_handlers":"16"'),
    json.replace('"mode":"sequential"', '"mode":["sequential"]'),
    json.replace('"max_handlers":16', '"max_handlers":16.0'),
    json.replace('"remote_ok":false', '"remote_ok":null'),
    json.replace('"budget_ms":1000', '"budget_ms":1e999'),
  ])("rejects untrusted raw catalog variation", (raw) => {
    expect(() => readCatalog(raw)).toThrow();
  });
  it("requires validators and copies declarations away from compiler callbacks", () => {
    expect(() => new Registry(json, () => ({ input: undefined! }))).toThrow();
    const r = new Registry(json, (d) => {
      (d as { name: string }).name = "changed";
      return {
        input: () => {},
        ...(d.kind === "filter" ? { output: () => {} } : {}),
      };
    });
    expect(
      JSON.parse(r.catalog()).definitions.some(
        (d: { name: string }) => d.name === "changed",
      ),
    ).toBe(false);
  });
  it("rejects runtime option coercion and foreign handles", () => {
    const r = registry();
    const a = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    const b = registry().newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    const h = a.addAction("event.observe", "h", {}, () => {});
    expect(b.remove(h)).toBe(false);
    expect(a.remove({} as typeof h)).toBe(false);
    for (const options of [
      { priority: null },
      { priority: "0" },
      { once: null },
      { once: 1 },
      { view: null },
      { timeoutMs: NaN },
      { onError: "" },
      { unknown: true },
    ])
      expect(() =>
        a.validateRegistration(
          "event.observe",
          "new",
          "action",
          options as unknown as Options,
        ),
      ).toThrow();
  });
});

it("rejects async validators rather than silently accepting a pending check", async () => {
  const r = new Registry(JSON.stringify(corpus.catalog), (d) => ({
    input: async () => {
      throw Error("rejected");
    },
    ...(d.kind === "filter" ? { output: () => {} } : {}),
  }));
  const engine = new Engine(r);
  await expect(engine.doAction("event.observe", "{}")).rejects.toMatchObject({
    code: "invalid_payload",
  });
  await engine.shutdown();
});

it("preserves numeric literals in schemas passed to compilers and catalog snapshots", () => {
  const json = JSON.stringify(corpus.catalog).replace(
    '"input_schema":{}',
    '"input_schema":{"const":9007199254740993}',
  );
  let schema = "";
  const r = new Registry(json, (d, schemas) => {
    if (d.name === "event.observe") schema = schemas.input;
    return {
      input: () => {},
      ...(d.kind === "filter" ? { output: () => {} } : {}),
    };
  });
  expect(schema).toBe('{"const":9007199254740993}');
  expect(r.catalog()).toContain('"const":9007199254740993');
});
