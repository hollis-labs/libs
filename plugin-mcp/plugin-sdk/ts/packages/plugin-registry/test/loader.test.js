import { test } from "node:test";
import assert from "node:assert/strict";
import { createPluginRegistry, bundleDigest } from "../dist/index.js";
import {
  registry,
  response,
  entry,
  bytes,
  digest,
  deferred,
  policy,
} from "./helpers.js";

test("verified bytes reach importer; undeclared exports confer no authority", async () => {
  const imports = [];
  const r = registry({
    importModule: async (bundle) => {
      imports.push(bundle);
      return { Panel: () => null, Extra: () => null };
    },
  });
  const result = await r.sync(response());
  assert.equal(result.accepted, true);
  assert.equal(result.resolved, 1);
  assert.equal(r.get("panel", "p/main").owner_id, "p");
  assert.equal(r.get("panel", "p/Extra"), undefined);
  assert.deepEqual(imports[0].bytes, bytes);
  assert.equal(imports[0].digest, await bundleDigest(bytes));
  assert.equal(imports[0].hostInstance, "epoch");
});
test("default import executes the verified self-contained module without URL refetch", async () => {
  const r = createPluginRegistry({
    ...policy,
    runtimes: { react: "19.0.0" },
    stylesheets: false,
    fetchBundle: async () => bytes,
  });
  assert.equal((await r.sync(response())).accepted, true);
  assert.equal(typeof r.get("panel", "p/main").value, "function");
});
test("integrity or runtime preflight failure preserves active generation", async () => {
  const r = registry();
  await r.sync(response());
  const old = r.get("panel", "p/main");
  for (const candidate of [response(2, "2"), response(3, "3")]) {
    if (candidate.revision === 2)
      candidate.plugins.p.bundle_version = digest(new Uint8Array([1]));
    else candidate.plugins.p.runtime[0].min = "20.0.0";
    assert.equal((await r.sync(candidate)).accepted, false);
    assert.equal(r.get("panel", "p/main"), old);
    assert.equal(old.isActive(), true);
  }
});
test("a failed preflight reservation does not poison retry at the same revision", async () => {
  let bad = true;
  const r = registry({
    fetchBundle: async () => (bad ? new Uint8Array([0]) : bytes),
  });
  assert.equal((await r.sync(response())).accepted, false);
  bad = false;
  assert.equal((await r.sync(response())).accepted, true);
});
test("unknown optional kinds degrade; required kinds fail before loading", async () => {
  let calls = 0;
  const r = registry({
    importModule: async () => {
      calls++;
      return { Panel: () => null };
    },
  });
  const c = response();
  c.contributions.future = {
    "p/x": entry({
      local: "x",
      kind: "future",
      representation: "declarative",
      required: false,
    }),
  };
  assert.equal((await r.sync(c)).accepted, true);
  assert.equal(r.refusals()[0].reason, "unsupported-kind");
  assert.equal(calls, 1);
  const next = response(2, "2");
  next.contributions.future = {
    "p/x": entry({
      local: "x",
      kind: "future",
      generation: "2",
      representation: "declarative",
    }),
  };
  assert.equal((await r.sync(next)).accepted, false);
  assert.equal(calls, 1);
  assert.equal(r.isActive("p", "1", "epoch"), true);
});
test("per-host kind/region opt-in and declarative-only boundaries are explicit", async () => {
  for (const options of [
    { kinds: {}, regions: {} },
    { regions: {} },
    {
      kinds: {
        panel: { ...policy.kinds.panel, representations: ["declarative"] },
      },
    },
  ]) {
    const r = registry(options);
    assert.equal((await r.sync(response())).accepted, false);
    assert.match(
      r
        .snapshot()
        .errors.map((e) => e.reason)
        .join(""),
      /^$/,
    );
  }
});
test("metadata schema cannot be silently ignored", async () => {
  const r = registry({
    kinds: {
      panel: { ...policy.kinds.panel, metadata_schema: { type: "object" } },
    },
  });
  assert.equal((await r.sync(response())).accepted, false);
});
test("regression: manifest-only addition/withdrawal changes entries without reimport", async () => {
  let imports = 0;
  const disposed = [];
  const r = registry({
    importModule: async () => {
      imports++;
      return { Panel: () => null, Extra: () => null };
    },
    dispose: (e) => disposed.push(e.local_key),
  });
  await r.sync(response());
  const next = response(2);
  next.contributions.panel["p/extra"] = entry({
    local: "extra",
    component: { export: "Extra", region: "rail" },
  });
  await r.sync(next);
  assert.ok(r.get("panel", "p/extra"));
  assert.equal(imports, 1);
  await r.sync(response(3));
  assert.equal(r.get("panel", "p/extra"), undefined);
  assert.deepEqual(disposed, ["extra"]);
  assert.equal(imports, 1);
});
test("regression: export names are never inferred; missing optional export is named", async () => {
  const c = response();
  c.contributions.panel["p/main"].component.export = "main";
  c.contributions.panel["p/main"].required = false;
  const r = registry();
  assert.equal((await r.sync(c)).accepted, true);
  assert.equal(r.get("panel", "p/main"), undefined);
  assert.equal(r.refusals()[0].reason, "export-missing");
  assert.equal(r.ownerOf("panel", "p/main"), "p");
});
test("regression: core and reserved claims cannot shadow the host", async () => {
  const r = registry({ reserved: (_kind, key) => key === "p/main" });
  assert.equal((await r.sync(response())).accepted, false);
  const c = response();
  c.plugins.core = c.plugins.p;
  delete c.plugins.p;
  c.contributions.panel = { "core/main": entry({ owner: "core" }) };
  assert.equal((await registry().sync(c)).accepted, false);
});
test("two owners may export the same name without colliding", async () => {
  const c = response();
  c.plugins.q = { ...c.plugins.p, bundle_url: "/q.js" };
  c.contributions.panel["q/main"] = entry({ owner: "q" });
  const r = registry();
  await r.sync(c);
  assert.equal(r.list("panel").length, 2);
  assert.equal(r.ownerOf("panel", "q/main"), "q");
});
test("revoke and dispose happen before replacement import; old captured entry is fenced", async () => {
  const log = [];
  let captured;
  const r = registry({
    importModule: async (b) => {
      log.push(`import-${b.generation}`);
      if (b.generation === "2") assert.equal(captured.isActive(), false);
      return { Panel: () => null };
    },
    dispose: (e) => {
      log.push(`dispose-${e.owner_generation}`);
      assert.equal(e.isActive(), false);
    },
  });
  await r.sync(response());
  captured = r.get("panel", "p/main");
  await r.sync(response(2, "2"));
  assert.deepEqual(log, ["import-1", "dispose-1", "import-2"]);
  assert.equal(captured.isActive(), false);
  assert.equal(r.get("panel", "p/main").isActive(), true);
});
test("post-revoke import failure never restores old generation", async () => {
  const r = registry({
    importModule: async (b) => {
      if (b.generation === "2") throw Error("load failure");
      return { Panel: () => null };
    },
  });
  await r.sync(response());
  assert.equal((await r.sync(response(2, "2"))).accepted, false);
  assert.equal(r.get("panel", "p/main"), undefined);
  assert.equal(r.isActive("p", "1", "epoch"), false);
  assert.equal((await r.sync(response(3, "1"))).accepted, false);
});
test("generation identity cannot be reused with a new digest", async () => {
  const r = registry();
  await r.sync(response());
  const c = response(2);
  c.plugins.p.bundle_version = digest(new Uint8Array([1]));
  assert.equal((await r.sync(c)).accepted, false);
  assert.ok(r.get("panel", "p/main"));
});
test("unchanged declarative data keeps value identity and one disposer", async () => {
  const c = response();
  delete c.plugins.p.bundle_url;
  delete c.plugins.p.bundle_version;
  c.contributions = {
    "nav.item": {
      "p/nav": entry({
        local: "nav",
        kind: "nav.item",
        representation: "declarative",
      }),
    },
  };
  const disposed = [];
  const r = registry({ dispose: (e) => disposed.push(e.value) });
  await r.sync(c);
  const old = r.get("nav.item", "p/nav");
  const next = structuredClone(c);
  next.revision++;
  next.contributions["nav.item"]["p/nav"].metadata = { label: "updated" };
  await r.sync(next);
  assert.equal(r.get("nav.item", "p/nav").value, old.value);
  assert.equal(old.isActive(), true);
  assert.equal(disposed.length, 0);
  await r.unload("p");
  assert.equal(disposed.length, 1);
  assert.equal(old.isActive(), false);
});
test("changed declarative values dispose previous resource before adoption", async () => {
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
  const log = [];
  const r = registry({ dispose: (e) => log.push(e.value.label) });
  await r.sync(c);
  const next = structuredClone(c);
  next.revision = 2;
  next.contributions["nav.item"]["p/nav"].declarative.label = "New";
  await r.sync(next);
  assert.deepEqual(log, ["Notes"]);
  assert.equal(r.get("nav.item", "p/nav").value.label, "New");
});
test("disposal continues in reverse acquisition order and quarantines failures", async () => {
  const c = response();
  c.contributions.panel["p/second"] = entry({ local: "second" });
  const log = [];
  const r = registry({
    dispose: (e) => {
      log.push(e.local_key);
      if (e.local_key === "second") throw Error("dispose failure");
    },
  });
  await r.sync(c);
  await r.unload("p");
  assert.deepEqual(log, ["second", "main"]);
  assert.equal((await r.sync(response(2, "2"))).accepted, false);
  assert.equal(r.errors()[0].stage, "dispose");
  await r.unload("p");
  assert.deepEqual(log, ["second", "main"]);
});
test("partial adoption failure rolls back every new resource without publication", async () => {
  const c = response();
  c.contributions.panel["p/second"] = entry({ local: "second" });
  const disposed = [];
  const r = registry({
    adopt: (e) => {
      if (e.local_key === "second") throw Error("adopt failure");
      return e.export;
    },
    dispose: (e) => disposed.push(e.local_key),
  });
  assert.equal((await r.sync(c)).accepted, false);
  assert.equal(r.list("panel").length, 0);
  assert.deepEqual(disposed, ["main"]);
});
test("superseded fetch/import cannot publish a stale revision", async () => {
  const gate = deferred();
  const started = deferred();
  const r = registry({
    importModule: async (b) => {
      if (b.generation === "1") {
        started.resolve();
        await gate.promise;
      }
      return { Panel: () => null };
    },
  });
  const old = r.sync(response());
  await started.promise;
  const newer = r.sync(response(2, "2"));
  gate.resolve();
  await Promise.all([old, newer]);
  assert.equal(r.isActive("p", "2", "epoch"), true);
  assert.equal(r.isActive("p", "1", "epoch"), false);
});
test("unload tombstones pending generations and aborts their cooperative import", async () => {
  const gate = deferred(),
    started = deferred();
  let signal;
  const r = registry({
    importModule: async (b) => {
      signal = b.signal;
      started.resolve();
      await gate.promise;
      return { Panel: () => null };
    },
  });
  const sync = r.sync(response());
  await started.promise;
  const unload = r.unload("p", "1");
  assert.equal(signal.aborted, true);
  gate.resolve();
  await Promise.all([sync, unload]);
  assert.equal(r.get("panel", "p/main"), undefined);
  assert.equal((await r.sync(response(2, "1"))).accepted, false);
});
test("stale revisions/epochs and reused revision content are refused unchanged", async () => {
  const r = registry();
  await r.sync(response(2));
  const old = r.get("panel", "p/main");
  for (const c of [
    response(1),
    { ...response(3), host_instance: "older-epoch" },
    {
      ...response(2),
      refusals: [
        {
          owner_id: "p",
          owner_generation: "1",
          kind: "future",
          local_key: "x",
          reason: "unsupported-kind",
          required: false,
        },
      ],
    },
  ])
    assert.equal((await r.sync(c)).accepted, false);
  assert.equal(r.get("panel", "p/main"), old);
  assert.equal((await r.sync(response(2))).accepted, true);
});
test("malformed protocol does not clear last known registry", async () => {
  const r = registry();
  await r.sync(response());
  for (const c of [null, {}, { ...response(2), registry_version: 1 }])
    assert.equal((await r.sync(c)).accepted, false);
  assert.ok(r.get("panel", "p/main"));
});
test("clear retains tombstones and requires a fresh epoch to reconnect", async () => {
  const r = registry();
  await r.sync(response());
  await r.clear();
  assert.equal((await r.sync(response(2))).accepted, false);
  const c = response();
  c.host_instance = "fresh-epoch";
  assert.equal((await r.sync(c)).accepted, true);
  assert.equal(r.isActive("p", "1", "epoch"), false);
});
test("subscribers and version references are stable; failed observers cannot stop notification", async () => {
  const r = registry();
  const sub = r.subscribe,
    version = r.version;
  let n = 0;
  r.subscribe(() => {
    throw Error("observer");
  });
  const stop = r.subscribe(() => n++);
  await r.sync(response());
  assert.equal(n, 1);
  assert.equal(r.subscribe, sub);
  assert.equal(r.version, version);
  stop();
  await r.unload("p");
  assert.equal(n, 1);
});
test("stylesheet withdrawal on unchanged generation removes the resource", async () => {
  const log = [];
  const r = registry({
    stylesheets: {
      ensure: (key, url) => log.push(["ensure", key, url]),
      remove: (key) => log.push(["remove", key]),
    },
  });
  const c = response();
  c.plugins.p.stylesheet_url = "/p.css";
  await r.sync(c);
  await r.sync(response(2));
  assert.equal(log[0][0], "ensure");
  assert.equal(log[1][0], "remove");
  assert.equal(log[0][1], log[1][1]);
});
test("inherited exports and policy map keys are never admitted", async () => {
  const r = registry({
    importModule: async () => Object.create({ Panel: () => null }),
  });
  assert.equal((await r.sync(response())).accepted, false);
  const inherited = registry({ kinds: Object.create(policy.kinds) });
  assert.equal((await inherited.sync(response())).accepted, false);
});

test("retiring an old generation does not tombstone its replacement", async () => {
  const r = registry();
  await r.sync(response());
  assert.equal((await r.sync(response(2, "2"))).accepted, true);
  const current = r.get("panel", "p/main");
  assert.equal((await r.sync(response(3, "2"))).accepted, true);
  assert.equal(current.isActive(), true);
});

test("inactive statuses stay listed without fetch, import or adoption", async () => {
  for (const status of [
    "declared_not_selected",
    "unavailable",
    "future_status",
  ]) {
    const diagnostics = [];
    const r = registry({
      fetchBundle: async () => {
        assert.fail("inactive bundle fetched");
      },
      importModule: async () => {
        assert.fail("inactive bundle imported");
      },
      adopt: () => {
        assert.fail("inactive contribution adopted");
      },
      onDiagnostic: (e) => diagnostics.push(e),
    });
    const c = response();
    c.contributions.panel["p/main"].status = status;
    c.contributions.panel["p/main"].status_reason = "host-choice";
    assert.equal((await r.sync(c)).accepted, true);
    assert.equal(r.get("panel", "p/main"), undefined);
    assert.equal(r.list("panel").length, 0);
    assert.equal(r.ownerOf("panel", "p/main"), "p");
    const listed = r.snapshot().contributions[0];
    assert.equal(listed.status, status);
    assert.equal(listed.status_reason, "host-choice");
    assert.equal(listed.resolved, false);
    assert.deepEqual(r.refusals(), []);
    assert.equal(diagnostics.length, status === "future_status" ? 1 : 0);
    if (diagnostics.length) {
      assert.equal(diagnostics[0].type, "status-diagnostic");
      assert.equal(diagnostics[0].diagnostic.reason, "unknown-status");
    }
  }
});
test("retained refused entry uses one authoritative top-level refusal", async () => {
  const c = response();
  c.contributions.panel["p/main"].required = false;
  c.contributions.panel["p/main"].status = "refused";
  c.contributions.panel["p/main"].status_reason = "host-denied";
  c.refusals.push({
    owner_id: "p",
    owner_generation: "1",
    kind: "panel",
    local_key: "main",
    required: false,
    reason: "host-denied",
  });
  const diagnostics = [];
  const r = registry({
    kinds: {},
    regions: {},
    fetchBundle: async () => {
      assert.fail("refused bundle fetched");
    },
    onDiagnostic: (e) => diagnostics.push(e),
  });
  assert.equal((await r.sync(c)).accepted, true);
  assert.equal(r.get("panel", "p/main"), undefined);
  assert.equal(r.snapshot().contributions[0].status, "refused");
  assert.equal(r.refusals().length, 1);
  assert.equal(
    diagnostics.filter((e) => e.type === "contribution-refused").length,
    1,
  );
});
test("inactive status cannot bypass required admission", async () => {
  for (const status of [
    "declared_not_selected",
    "unavailable",
    "future_status",
  ]) {
    const c = response();
    c.contributions.panel["p/main"].status = status;
    const r = registry({ kinds: {}, regions: {} });
    assert.equal((await r.sync(c)).accepted, false);
  }
});
test("status withdrawal fences and disposes before the same generation is selected again", async () => {
  const disposed = [];
  let imports = 0;
  const r = registry({
    importModule: async () => {
      imports++;
      return { Panel: () => null };
    },
    dispose: (e) => disposed.push(e.local_key),
  });
  await r.sync(response());
  const captured = r.get("panel", "p/main");
  const inactive = response(2);
  inactive.contributions.panel["p/main"].status = "declared_not_selected";
  assert.equal((await r.sync(inactive)).accepted, true);
  assert.equal(captured.isActive(), false);
  assert.deepEqual(disposed, ["main"]);
  assert.equal((await r.sync(response(3))).accepted, true);
  assert.equal(r.get("panel", "p/main").isActive(), true);
  assert.equal(captured.isActive(), false);
  assert.equal(imports, 1);
});

test("raw decimal revision is refused before normalization and preserves the serving registry", async () => {
  const diagnostics = [];
  const r = registry({ onDiagnostic: (e) => diagnostics.push(e) });
  await r.sync(response());
  const captured = r.get("panel", "p/main");
  const raw = JSON.stringify(response(2)).replace(
    '"revision":2',
    '"revision":2.0',
  );
  assert.equal((await r.sync(raw)).accepted, false);
  assert.equal(r.get("panel", "p/main"), captured);
  assert.equal(captured.isActive(), true);
  assert.equal(diagnostics.at(-1).reason, "ErrInvalidContribution");
});
