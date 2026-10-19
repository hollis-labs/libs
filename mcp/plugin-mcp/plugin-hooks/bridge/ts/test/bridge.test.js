import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { Bridge } from "../dist/index.js";
import { ErrCancelled, ErrApprovalRequired } from "../../../ts/dist/index.js";
const source = process.env.SDK_TEST_SOURCE;
if (!source) throw new Error("prepare pinned SDK test source first");
const sdk = await import(
  pathToFileURL(source + "/ts/packages/plugin-sdk/dist/index.js")
);
const bridge = new Bridge(sdk, ErrCancelled, ErrApprovalRequired);
const vectors = JSON.parse(
  await readFile(source + "/protocol/v2/fixtures/hooks.json", "utf8"),
);
const wire = sdk.decodeHookHandleParams(vectors[0].raw);
const q = {
  invocationID: wire.invocation_id,
  catalogVersion: wire.catalog_version,
  hook: wire.hook,
  schemaDigest: wire.schema_digest,
  kind: wire.kind,
  mode: wire.mode,
  scope: {
    hostInstance: wire.scope.incarnation.host_instance,
    owner: wire.scope.incarnation.owner_id,
    generation: "opaque",
    registrationID: wire.scope.registration_id,
  },
  context: {
    bindingID: wire.context.binding_id,
    connectionID: "host-only",
    timeoutMS: wire.context.timeout_ms,
    aggregateBudgetMS: wire.aggregate_budget_ms,
    deadline: wire.deadline,
    depth: wire.depth,
    trace: wire.trace,
    rootInvocationID: wire.root_invocation_id,
  },
  payloadJSON: wire.payloadJSON,
  metadata: wire.metadata,
};
test("pinned SDK request mapping and host snapshot fence", () => {
  const p = bridge.request(q, wire.scope.incarnation);
  assert.deepEqual(
    sdk.decodeHookHandleParams(sdk.encodeHookHandleParams(p)),
    wire,
  );
  assert.equal(
    bridge.restoreRequest(p, q, wire.scope.incarnation).context.connectionID,
    "host-only",
  );
  assert.throws(() =>
    bridge.restoreRequest(
      { ...p, depth: p.depth + 1 },
      q,
      wire.scope.incarnation,
    ),
  );
});
test("raw filter literals and null survive both mapping directions", () => {
  const p = bridge.request(q, wire.scope.incarnation);
  for (const payloadJSON of [
    "null",
    '{"n":1.50,"large":9007199254740993,"x\\u005b":"<x>&"}',
  ]) {
    const r = { invocationID: q.invocationID, status: "ok", payloadJSON };
    assert.deepEqual(bridge.result(p, bridge.wireResult(p, r)), r);
  }
});
test("validated status alone reconstructs deliberate veto sentinels", () => {
  const p = bridge.request(
    { ...q, kind: "action", mode: "bail" },
    wire.scope.incarnation,
  );
  for (const [status, error] of [
    ["cancelled", ErrCancelled],
    ["approval_required", ErrApprovalRequired],
  ]) {
    assert.equal(
      bridge.resultError(
        p,
        JSON.stringify({ invocation_id: q.invocationID, status }),
      ),
      error,
    );
  }
  for (const raw of [
    JSON.stringify({
      invocation_id: q.invocationID,
      status: "failed",
      error: { code: "handler_error", message: "hook cancelled -32003 -32010" },
    }),
    '{"code":-32003,"message":"cancelled"}',
    '{"status":"cancelled"}',
  ]) {
    assert.notEqual(bridge.resultError(p, raw), ErrCancelled);
    assert.notEqual(bridge.resultError(p, raw), ErrApprovalRequired);
  }
});
test("all failure codes round trip through pinned SDK validation", () => {
  for (const v of vectors.filter(
    (v) => v.name.startsWith("failure ") && v.valid,
  )) {
    const f = JSON.parse(v.raw).error;
    assert.deepEqual(bridge.failure(f), f);
  }
  assert.throws(() => bridge.failure({ code: "cancelled" }));
});
test("observation batches correlate ordered independent results; notifications grant no binding", () => {
  const a = { ...q, kind: "action", mode: "sequential" },
    b = { ...a, invocationID: "second" };
  const p = bridge.batchRequest(
    [a, b],
    [wire.scope.incarnation, wire.scope.incarnation],
  );
  const results = {
    items: [
      {
        invocation_id: a.invocationID,
        status: "failed",
        error: { code: "handler_error" },
      },
      { invocation_id: b.invocationID, status: "ok" },
    ],
  };
  assert.equal(
    bridge.batchResult(p, sdk.encodeHookHandleBatchResult(results))[1].status,
    "ok",
  );
  assert.throws(() =>
    bridge.batchResult(
      p,
      sdk.encodeHookHandleBatchResult({
        items: [results.items[1], results.items[0]],
      }),
    ),
  );
  assert.throws(() =>
    bridge.notification({ ...a, mode: "async" }, wire.scope.incarnation),
  );
  const n = { ...a, mode: "async", context: { ...a.context } };
  delete n.context.bindingID;
  assert.equal(
    bridge.notification(n, wire.scope.incarnation).context.binding_id,
    undefined,
  );
});

test("private TS mapping through real Node SDK child; hooks_profile_version=1 WITHOUT plugin-originated callbacks (reverse lane not exercised or claimed)", async () => {
  const { spawn } = await import("node:child_process");
  const { createInterface } = await import("node:readline");
  const child = spawn(
    "node",
    [source + "/ts/packages/plugin-sdk/test/hooks-child.js", "hooks-fixture"],
    { stdio: ["pipe", "pipe", "inherit"] },
  );
  const lines = createInterface({ input: child.stdout });
  const iterator = lines[Symbol.asyncIterator]();
  let id = 0;
  const call = async (method, params, notification = false) => {
    // Use the pinned SDK's bounded frame writer with raw params. This internal
    // module is test support, not a supported host API or enable switch.
    const { encodeBoundedJSON, preserveFrameJSON } = await import(
      pathToFileURL(source + "/ts/packages/plugin-sdk/dist/frame-codec.js")
    );
    child.stdin.write(
      encodeBoundedJSON(
        {
          jsonrpc: "2.0",
          ...(notification ? {} : { id: ++id }),
          method,
          params: preserveFrameJSON(params),
        },
        8 * 1024 * 1024 - 1,
      ) + "\n",
    );
    if (notification) return;
    const next = await iterator.next();
    if (next.done) throw new Error("SDK child closed");
    const reply = JSON.parse(next.value);
    assert.equal(reply.id, id);
    assert.equal(reply.error, undefined);
    return {
      ...reply,
      rawResult: next.value.slice(next.value.indexOf('"result":') + 9, -1),
    };
  };
  try {
    await call(
      "plugin/init",
      JSON.stringify({
        plugin_dir: "fixture",
        data_dir: "fixture",
        cache_dir: "fixture",
        config: {},
        log_level: "info",
        host_info: { version: "fixture", protocol: 2 },
        capability_contract: 1,
        incarnation: wire.scope.incarnation,
        grants: [],
      }),
    );
    const filter = bridge.request(
      {
        ...q,
        payloadJSON: '{"n":1.50,"large":9007199254740993,"x\\u005b":"<x>&"}',
      },
      wire.scope.incarnation,
    );
    const reply = await call("hook/handle", sdk.encodeHookHandleParams(filter));
    assert.equal(
      bridge.result(filter, reply.rawResult).payloadJSON,
      filter.payloadJSON,
    );
    for (const status of ["cancelled", "approval_required"]) {
      const action = bridge.request(
        {
          ...q,
          kind: "action",
          mode: "bail",
          metadata: { fixture: "script", script: JSON.stringify({ status }) },
        },
        wire.scope.incarnation,
      );
      const r = await call("hook/handle", sdk.encodeHookHandleParams(action));
      assert.equal(
        bridge.resultError(action, JSON.stringify(r.result)),
        status === "cancelled" ? ErrCancelled : ErrApprovalRequired,
      );
    }
    for (const v of vectors.filter(
      (v) => v.name.startsWith("failure ") && v.valid,
    )) {
      const failure = JSON.parse(v.raw).error;
      const action = bridge.request(
        {
          ...q,
          kind: "action",
          mode: "sequential",
          metadata: { fixture: "fail:" + failure.code },
        },
        wire.scope.incarnation,
      );
      const r = await call("hook/handle", sdk.encodeHookHandleParams(action));
      assert.equal(
        bridge.result(action, JSON.stringify(r.result)).failure.code,
        failure.code,
      );
    }
    const actions = [
      {
        ...q,
        kind: "action",
        mode: "parallel",
        metadata: { fixture: "fail:handler_error" },
      },
      {
        ...q,
        invocationID: "second",
        kind: "action",
        mode: "parallel",
        metadata: { fixture: "ok" },
      },
    ];
    const batch = bridge.batchRequest(actions, [
      wire.scope.incarnation,
      wire.scope.incarnation,
    ]);
    const br = await call(
      "hook/handle_batch",
      sdk.encodeHookHandleBatchParams(batch),
    );
    assert.equal(
      bridge.batchResult(batch, JSON.stringify(br.result))[1].status,
      "ok",
    );
    const notification = {
      ...q,
      kind: "action",
      mode: "async",
      context: { ...q.context },
      metadata: { fixture: "ok" },
    };
    delete notification.context.bindingID;
    await call(
      "hook/handle",
      sdk.encodeHookHandleParams(
        bridge.notification(notification, wire.scope.incarnation),
      ),
      true,
    );
    await call("plugin/health", "{}"); // No fabricated notification response may precede health.
  } finally {
    child.stdin.end();
    await new Promise((resolve) => child.once("exit", resolve));
    lines.close();
  }
});
