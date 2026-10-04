// Shared expanded child scenarios. All SDK traffic uses the existing bounded parent.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { setTimeout as delay } from "node:timers/promises";
import { Writable } from "node:stream";
import { FrameWriter } from "../dist/publication.js";
import { ChildSession } from "./child-parent.js";
import { childSpec } from "./child-spec.js";
import { decodeHostRPCDTO } from "../dist/host-rpc.js";
import { inspectEnvelope, parseJSONTokens } from "../dist/strict-json.js";
const manifest = JSON.parse(
  await readFile(
    new URL(
      "../../../../protocol/v2/fixtures/duplex-child.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
let parentSignal, negotiatedMode = false;
const template = JSON.parse(
  await readFile(
    new URL(
      "../../../../protocol/v2/fixtures/host-storage.json",
      import.meta.url,
    ),
    "utf8",
  ),
).init;
export class TypedFixtureHost {
  constructor() {
    this.receipts = new Map();
    this.executions = 0;
    this.calls = 0;
  }
  request(frame) {
    const v = frame.value;
    assert.ok(["host/storage/get", "host/storage/put"].includes(v.method));
    const dto = v.method.endsWith("/get")
      ? "StorageGetParams"
      : "StoragePutParams";
    const p = decodeHostRPCDTO(
      dto,
      inspectEnvelope(frame.raw).fields.get("params"),
    );
    assert.equal(p.context.binding_id, "binding-example");
    assert.equal(p.context.parent_call.request_owner, "host");
    this.calls++;
    return { ...v, params: p };
  }
  commit(req, generation = 1) {
    const p = req.params,
      key = JSON.stringify(["fixture", req.method, p.key, p.operation_key]);
    let receipt = this.receipts.get(key);
    if (!receipt) {
      this.executions++;
      receipt = {
        generation,
        operation_key: p.operation_key,
        revision: "r" + this.executions,
      };
      this.receipts.set(key, receipt);
    }
    return { operation_key: receipt.operation_key, revision: receipt.revision };
  }
  async reply(s, req, result) {
    decodeHostRPCDTO(
      req.method.endsWith("/get") ? "StorageGetResult" : "StoragePutResult",
      JSON.stringify(result),
    );
    await s.send(JSON.stringify({ jsonrpc: "2.0", id: req.id, result }));
  }
  async fault(s, req, code, effect_state) {
    const data = {
      contract: "host-rpc/1",
      code,
      request_id: req.id,
      effect_state,
      retryable: false,
    };
    decodeHostRPCDTO("HostRPCErrorData", JSON.stringify(data));
    await s.send(
      JSON.stringify({
        jsonrpc: "2.0",
        id: req.id,
        error: { code: -32010, message: "fixture classified failure", data },
      }),
    );
  }
}
function context(timeout_ms = 10000) {
  return { binding_id: "binding-example", timeout_ms };
}
async function send(s, id, method, params = {}) {
  await s.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
}
async function command(s, id, name, args = {}, timeout = 10000) {
  await send(s, id, "command/execute", {
    name,
    args: JSON.stringify(args),
    session_id: "",
    ...(timeout === null ? {} : { context: context(timeout) }),
  });
  return s.event((e) => e.kind === "entered" && e.id === id);
}
async function response(s, id) {
  return (await s.frame((v) => v.id === id && !v.method)).value;
}
async function snapshot(s) {
  await s.release("snapshot");
  return (await s.event((e) => e.kind === "snapshot")).effects;
}
async function release(s, id) {
  await s.release("request-" + id);
  await s.event((e) => e.kind === "returned" && e.id === id);
}
async function cancelled(s, id, reason = "caller_cancelled", owner = "host") {
  await s.send(
    JSON.stringify({
      jsonrpc: "2.0",
      method: "rpc/cancel",
      params: { request_owner: owner, id, reason },
    }),
  );
}
function classified(v, code, state) {
  assert.ok(v.error);
  assert.equal(v.error.data.code, code);
  assert.equal(v.error.data.effect_state, state);
  assert.equal(v.error.data.retryable, false);
}
async function finish(s, success = true) {
  await s.end();
  const e = await s.event((e) => e.kind === "finished");
  const exit = await s.wait();
  assert.equal(exit.signal, null);
  assert.equal(exit.code, success ? 0 : 1);
  assert.equal(e.effects.unload_attempts, 1);
  if (s.failure) throw s.failure;
  assert.equal(s.frames.items.length, 0);
  return e;
}
async function start(runtime, goChild, profile, generation = 1) {
  const spec = childSpec(runtime, negotiatedMode ? "negotiated:" + profile : profile, goChild);
  const s = await ChildSession.start(spec.command, spec.args, {
    ...spec.options,
    signal: parentSignal,
  });
  try {
    const init = structuredClone(template);
    init.incarnation.owner_generation = generation;
    init.host_services.incarnation.owner_generation = generation;
    for (const g of init.grants) g.owner_generation = generation;
    // Long numbers here are fixture-owned offers, never SDK fallback deadlines.
    for (const m of init.host_services.methods)
      init.host_services.limits.method_timeout_ms[m] = 10000;
    await send(s, 1, "plugin/init", init);
    const r = await response(s, 1);
    assert.equal(r.result.protocol, 2);
    assert.equal(r.result.reverse_rpc_version, negotiatedMode ? 1 : undefined);
    return s;
  } catch (error) {
    await s.dispose();
    throw error;
  }
}
async function forward(s) {
  for (let id = 2; id < 18; id++) await command(s, id, "hold", {}, null);
  await send(s, 18, "command/execute", {
    name: "hold",
    args: "{}",
    session_id: "",
  });
  classified(await response(s, 18), "rate_limited", "not_started");
  assert.equal((await snapshot(s)).hold, 16);
  // Control has its own two permits while all 16 ordinary callbacks are held.
  for (let id = 19; id <= 20; id++) {
    await send(s, id, "plugin/load");
    await s.event((e) => e.kind === "entered" && e.id === id);
  }
  await send(s, 21, "plugin/load");
  classified(await response(s, 21), "rate_limited", "not_started");
  assert.equal((await snapshot(s)).load, 2);
  for (let id = 19; id <= 20; id++) {
    await release(s, id);
    assert.deepEqual((await response(s, id)).result, {});
  }
  for (let id = 2; id < 18; id++) {
    await release(s, id);
    assert.deepEqual((await response(s, id)).result, { action: "noop" });
  }
  await finish(s);
}
async function reverse(s, host) {
  await command(s, 2, "get", { n: 9, key: "read" });
  const requests = [];
  for (let i = 0; i < 8; i++)
    requests.push(
      host.request(await s.frame((v) => v.method === "host/storage/get")),
    );
  const refusal = await s.event(
    (e) =>
      e.kind === "helper_done" &&
      e.id === 2 &&
      e.failure.code === "rate_limited",
  );
  assert.equal(refusal.failure.effect_state, "not_started");
  assert.equal(host.calls, 8);
  for (const req of requests) await host.reply(s, req, { found: false });
  const results = JSON.parse((await response(s, 2)).result.content);
  assert.equal(results.filter((r) => r.code === "ok").length, 8);
  assert.equal(results.filter((r) => r.code === "rate_limited").length, 1);
  await finish(s);
}
async function baseCancel(s) {
  for (let n = 0; n < 16; n++)
    await command(s, "held-" + n, "uncooperative", {}, null);
  // Bad owner/case/id/reason/extra fields cannot terminate a callback.
  for (const params of [
    { request_owner: "plugin", id: "held-0", reason: "caller_cancelled" },
    { request_owner: "host", id: null, reason: "caller_cancelled" },
    { request_owner: "host", id: "held-0", reason: "invented" },
    {
      request_owner: "host",
      id: "held-0",
      reason: "caller_cancelled",
      extra: 1,
    },
    { Request_owner: "host", id: "held-0", reason: "caller_cancelled" },
  ])
    await s.send(
      JSON.stringify({ jsonrpc: "2.0", method: "rpc/cancel", params }),
    );
  await send(s, "barrier", "plugin/health");
  classified(await response(s, "barrier"), "rate_limited", "not_started");
  assert.equal(s.frames.items.length, 0);
  assert.equal((await snapshot(s)).returned ?? 0, 0);
  await cancelled(s, "held-0");
  classified(await response(s, "held-0"), "unknown_outcome", "unknown");
  // Same base ID is reusable after terminal receipt, but old execution keeps its permit.
  await send(s, "held-0", "plugin/health");
  classified(await response(s, "held-0"), "rate_limited", "not_started");
  assert.equal((await snapshot(s)).health ?? 0, 0);
  await release(s, "held-0");
  await send(s, "held-0", "plugin/health");
  assert.equal((await response(s, "held-0")).result.ok, true);
  for (let n = 1; n < 16; n++) {
    await release(s, "held-" + n);
    await response(s, "held-" + n);
  }
  await finish(s);
}
async function descendants(s, host) {
  const requests = [];
  for (const id of [2, 3]) {
    await command(s, id, "get", { n: 2, key: "read" });
    for (let n = 0; n < 2; n++)
      requests.push(
        host.request(await s.frame((v) => v.method === "host/storage/get")),
      );
  }
  await cancelled(s, 2);
  classified(await response(s, 2), "unknown_outcome", "unknown");
  const childIDs = requests
    .filter((r) => r.params.context.parent_call.id === 2)
    .map((r) => r.id)
    .sort();
  const cancelledIDs = [];
  for (let n = 0; n < 2; n++) {
    const p = (await s.frame((v) => v.method === "rpc/cancel")).value.params;
    assert.equal(p.request_owner, "plugin");
    assert.equal(p.reason, "parent_cancelled");
    cancelledIDs.push(p.id);
  }
  assert.deepEqual(cancelledIDs.sort(), childIDs);
  for (const req of requests) await host.reply(s, req, { found: false });
  const surviving = JSON.parse((await response(s, 3)).result.content);
  assert.ok(surviving.every((r) => r.code === "ok"));
  await finish(s);
}
async function deadline(s) {
  const e = await command(s, 2, "uncooperative", {}, 300);
  assert.equal(e.deadline, true);
  classified(await response(s, 2), "unknown_outcome", "unknown");
  assert.equal(
    (await s.event((e) => e.kind === "aborted" && e.id === 2)).deadline,
    true,
  );
  assert.equal((await snapshot(s)).returned ?? 0, 0);
  await release(s, 2);
  const noDeadline = await command(s, 3, "hold", {}, null);
  assert.equal(noDeadline.deadline, false);
  await delay(100);
  await send(s, 4, "plugin/health");
  assert.equal((await response(s, 4)).result.ok, true);
  assert.equal(s.frames.items.length, 0);
  await release(s, 3);
  await response(s, 3);
  await finish(s);
}
async function clipped(s, host) {
  await s.release("arm-writer");
  await send(s, 2, "plugin/health");
  await s.event((e) => e.kind === "writer_waiting");
  await command(s, 3, "get", { n: 1, key: "read" }, 5000);
  await until(s, (v) => v.ordinary_queued === 1 && v.reverse_pending === 1);
  await delay(250);
  await s.release("writer");
  await response(s, 2);
  const req = host.request(
    await s.frame((v) => v.method === "host/storage/get"),
  );
  assert.ok(
    req.params.context.timeout_ms > 0 && req.params.context.timeout_ms <= 4800,
  );
  await host.reply(s, req, { found: false });
  await response(s, 3);
  await finish(s);
}
async function effects(s, host, unknown = false) {
  await command(s, 2, "put", {
    n: 1,
    key: "write",
    operation_key: "stable-key",
  });
  const req = host.request(
    await s.frame((v) => v.method === "host/storage/put"),
  );
  const receipt = host.commit(req);
  if (unknown) {
    host.receipts.clear();
    await host.fault(s, req, "unknown_outcome", "unknown");
  } else await host.reply(s, req, receipt);
  const result = JSON.parse((await response(s, 2)).result.content)[0];
  assert.equal(result.code, unknown ? "unknown_outcome" : "ok");
  if (unknown) assert.equal(result.effect_state, "unknown");
  await send(s, 3, "plugin/health");
  await response(s, 3);
  assert.equal(host.calls, 1);
  assert.equal(host.executions, 1);
  assert.equal(s.frames.items.length, 0);
  await finish(s);
}
async function overflow(s) {
  await command(s, 2, "overflow");
  classified(await response(s, 2), "budget_exceeded", "committed");
  assert.equal((await snapshot(s)).commits, 1);
  await finish(s);
}
async function queue(s) {
  await s.release("arm-writer");
  await send(s, 2, "plugin/health");
  await s.event((e) => e.kind === "writer_waiting");
  await command(s, 3, "put", {
    n: 1,
    key: "write",
    operation_key: "queued",
    value_bytes: 65536,
  });
  const e = await s.event((e) => e.kind === "helper_done");
  assert.equal(e.failure.code, "rate_limited");
  assert.equal(e.failure.effect_state, "not_started");
  await s.release("writer");
  await response(s, 2);
  const r = JSON.parse((await response(s, 3)).result.content)[0];
  assert.equal(r.code, "rate_limited");
  assert.equal(s.frames.items.length, 0);
  await finish(s);
}
async function lifecycle(s, scenario) {
  if (scenario === "hung-callback") {
    await command(s, 2, "hung", {}, null);
    await s.end();
    const e = await s.event((e) => e.kind === "finished");
    assert.ok(e.transport_error);
    const exit = await s.wait();
    assert.equal(exit.code, 1);
    assert.equal(e.effects.unload_attempts ?? 0, 0);
  } else if (scenario === "disconnect") {
    await command(s, 2, "get", { n: 1, key: "read" });
    await s.frame((v) => v.method === "host/storage/get");
    await s.end();
    const r = JSON.parse((await response(s, 2)).result.content)[0];
    assert.equal(r.code, "target_unavailable");
    await finish(s);
  } else {
    await send(s, 2, "plugin/unload");
    if (scenario === "cleanup-hung") {
      const e = await s.event((e) => e.kind === "finished");
      assert.ok(e.transport_error);
      assert.equal((await s.wait()).code, 1);
      assert.equal(e.effects.unload_attempts, 1);
    } else {
      const r = await response(s, 2);
      assert.equal(r.error.code, -32603);
      const e = await s.event((e) => e.kind === "finished");
      assert.equal(e.effects.unload_attempts, 1);
      assert.equal((await s.wait()).code, 0);
    }
  }
}
async function until(s, predicate) {
  for (let i = 0; i < 40; i++) {
    const value = await snapshot(s);
    if (predicate(value)) return value;
    await delay(5);
  }
  assert.fail("fixture did not reach queue barrier");
}
async function queueFrames(s, host) {
  await s.release("arm-writer");
  await send(s, 2, "plugin/health");
  await s.event((e) => e.kind === "writer_waiting");
  await command(s, 3, "get", { n: 4, key: "read" });
  const refusal = await s.event(
    (e) => e.kind === "helper_done" && e.failure.code === "rate_limited",
  );
  assert.equal(refusal.failure.effect_state, "not_started");
  const state = await until(s, (v) => v.ordinary_queued === 3);
  assert.ok(state.control_queued + state.reserved_frames <= 3);
  await s.release("writer");
  await response(s, 2);
  for (let n = 0; n < 3; n++) {
    const req = host.request(
      await s.frame((v) => v.method === "host/storage/get"),
    );
    await host.reply(s, req, { found: false });
  }
  const results = JSON.parse((await response(s, 3)).result.content);
  assert.equal(results.filter((v) => v.code === "ok").length, 3);
  assert.equal(results.filter((v) => v.code === "rate_limited").length, 1);
  assert.equal(host.calls, 3);
  await finish(s);
}
async function terminalCredits(s, host) {
  for (let id = 2; id <= 16; id++) await command(s, id, "hold", {}, null);
  await command(s, 17, "get", { n: 8, key: "read" });
  const requests = [];
  for (let i = 0; i < 8; i++)
    requests.push(
      host.request(await s.frame((v) => v.method === "host/storage/get")),
    );
  for (let id = 18; id <= 19; id++) {
    await send(s, id, "plugin/load");
    await s.event((e) => e.kind === "entered" && e.id === id);
  }
  const v = await snapshot(s);
  assert.equal(v.entered, 18);
  assert.equal(v.reverse_pending, 8);
  assert.equal(v.entered + v.reverse_pending, 16 + 8 + 2);
  assert.ok(v.entered + v.reverse_pending < 32);
  assert.equal(v.reserved_frames, 18);
  assert.equal(v.reserved_bytes, 18 * 1024);
  await send(s, 20, "command/execute", {
    name: "hold",
    args: "{}",
    session_id: "",
  });
  classified(await response(s, 20), "rate_limited", "not_started");
  for (const req of requests) await host.reply(s, req, { found: false });
  await response(s, 17);
  for (let id = 18; id <= 19; id++) {
    await release(s, id);
    await response(s, id);
  }
  for (let id = 2; id <= 16; id++) {
    await release(s, id);
    await response(s, id);
  }
  await finish(s);
}
async function childFairness(s, host) {
  await s.release("arm-writer");
  await send(s, 2, "plugin/health");
  await s.event((e) => e.kind === "writer_waiting");
  await command(s, 3, "get", { n: 8, key: "read" });
  await until(s, (v) => v.ordinary_queued === 8);
  const controls = new Set([2]);
  for (let id = 4; id <= 15; id++) {
    controls.add(id);
    await send(s, id, "plugin/health");
  }
  const state = await until(s, (v) => v.control_queued === 12);
  assert.ok(state.reserved_frames + state.control_queued <= 32);
  assert.equal(state.reserved_bytes, state.reserved_frames * 1024);
  await s.release("writer");
  let ordinary = 0,
    burst = 0,
    extra = 0,
    next = 16,
    done = false;
  while (ordinary < 8 || controls.size || !done) {
    const frame = await s.frame(),
      v = frame.value;
    if (v.method) {
      const req = host.request(frame);
      ordinary++;
      burst = 0;
      await host.reply(s, req, { found: false });
    } else if (v.id === 3) {
      done = true;
      assert.ok(JSON.parse(v.result.content).every((r) => r.code === "ok"));
    } else {
      assert.ok(controls.delete(v.id));
      burst++;
      if (ordinary < 8)
        assert.ok(
          burst <= 4,
          "ordinary reverse lane starved by control replies",
        );
      if (extra++ < 8) {
        controls.add(next);
        await send(s, next++, "plugin/health");
      }
    }
  }
  await finish(s);
}
async function hostFairness(s, host) {
  await command(s, 2, "get", { n: 8, key: "read" });
  const requests = [];
  for (let n = 0; n < 8; n++)
    requests.push(
      host.request(await s.frame((v) => v.method === "host/storage/get")),
    );
  let open,
    writer,
    controlWrites = 0,
    failure;
  const blocked = new Promise((r) => (open = r)),
    order = [],
    receipts = [];
  const output = new Writable({
    write(raw, _enc, done) {
      void (async () => {
        await blocked;
        const v = parseJSONTokens(raw.toString());
        order.push(
          v.method && v.method !== "rpc/cancel" ? "ordinary" : "control",
        );
        await s.write(raw);
        if (!v.method || v.method === "rpc/cancel") {
          controlWrites++;
          if (controlWrites < 32) {
            const p = writer.publish(
              JSON.stringify({
                jsonrpc: "2.0",
                method: "rpc/cancel",
                params: {
                  request_owner: "host",
                  id: 9999,
                  reason: "caller_cancelled",
                },
              }) + "\n",
              "control",
            );
            receipts.push(p);
            void p.catch((e) => (failure ??= e));
          }
        }
      })().then(() => done(), done);
    },
  });
  writer = new FrameWriter(output, 5000, (e) => (failure ??= e));
  for (let id = 3; id <= 11; id++)
    receipts.push(
      writer.publish(
        JSON.stringify({
          jsonrpc: "2.0",
          id,
          method: "plugin/health",
          params: {},
        }) + "\n",
      ),
    );
  for (const req of requests) {
    const result = { found: false };
    decodeHostRPCDTO("StorageGetResult", JSON.stringify(result));
    receipts.push(
      writer.publish(
        JSON.stringify({ jsonrpc: "2.0", id: req.id, result }) + "\n",
        "control",
      ),
    );
  }
  open();
  await Promise.all(receipts);
  await writer.flush();
  if (failure) throw failure;
  output.destroy();
  let burst = 0,
    ordinary = 0;
  for (const lane of order) {
    if (lane === "ordinary") {
      ordinary++;
      burst = 0;
    } else {
      burst++;
      if (ordinary < 9)
        assert.ok(
          burst <= 4,
          "ordinary forward lane starved by host control traffic",
        );
    }
  }
  assert.equal(ordinary, 9);
  assert.ok(controlWrites >= 32);
  const seen = new Set();
  for (let n = 0; n < 10; n++) {
    const v = (await s.frame()).value;
    seen.add(v.id);
    assert.ok(v.result);
  }
  assert.equal(seen.size, 10);
  await finish(s);
}
async function receiptRestart(s, host, runtime, goChild) {
  await effects(s, host);
  const again = await start(runtime, goChild, "expanded", 2);
  try {
    await command(again, 2, "put", {
      n: 1,
      key: "write",
      operation_key: "stable-key",
    });
    const req = host.request(
      await again.frame((v) => v.method === "host/storage/put"),
    );
    await host.reply(again, req, host.commit(req, 2));
    await response(again, 2);
    assert.equal(host.executions, 1);
    assert.equal(host.calls, 2);
    assert.equal([...host.receipts.values()][0].generation, 1);
    await finish(again);
  } finally {
    await again.dispose();
  }
}
export async function replayCases(runtime, goChild, names, signal, negotiated = false) {
  parentSignal = signal;
  negotiatedMode = negotiated;
  const results = [];
  for (const recipe of manifest.expanded) {
    if (names && !names.includes(recipe.name)) continue;
    if (negotiated && recipe.scenario === "base-cancel") continue;
    assert.ok(["observed", "proposed"].includes(recipe.status ?? "observed"));
    assert.equal(recipe.level, "normative");
    if (recipe.status === "proposed") {
      assert.ok(recipe.owner?.trim() && recipe.reason?.trim());
      results.push({
        case: recipe.name,
        status: "unavailable",
        owner: recipe.owner,
        reason: recipe.reason,
      });
      continue;
    }
    assert.ok(
      [
        "forward",
        "reverse",
        "base-cancel",
        "descendants",
        "deadline",
        "clip",
        "effects",
        "unknown",
        "overflow",
        "queue",
        "child-fairness",
        "host-fairness",
        "credits",
        "receipt-restart",
        "queue-frames",
        "cleanup-error",
        "cleanup-panic",
        "cleanup-hung",
        "hung-callback",
        "disconnect",
      ].includes(recipe.scenario),
      "unknown child scenario",
    );
    let s;
    const host = new TypedFixtureHost();
    try {
      s = await start(runtime, goChild, recipe.profile);
      switch (recipe.scenario) {
        case "forward":
          await forward(s);
          break;
        case "reverse":
          await reverse(s, host);
          break;
        case "base-cancel":
          await baseCancel(s);
          break;
        case "descendants":
          await descendants(s, host);
          break;
        case "deadline":
          await deadline(s);
          break;
        case "clip":
          await clipped(s, host);
          break;
        case "effects":
          await effects(s, host);
          break;
        case "unknown":
          await effects(s, host, true);
          break;
        case "overflow":
          await overflow(s);
          break;
        case "queue":
          await queue(s);
          break;
        case "child-fairness":
          await childFairness(s, host);
          break;
        case "host-fairness":
          await hostFairness(s, host);
          break;
        case "queue-frames":
          await queueFrames(s, host);
          break;
        case "credits":
          await terminalCredits(s, host);
          break;
        case "receipt-restart":
          await receiptRestart(s, host, runtime, goChild);
          break;
        default:
          await lifecycle(s, recipe.scenario);
      }
      results.push({
        case: recipe.name,
        status: "passed",
        mode: negotiated ? "normal-serve-negotiated" : "internal-test-only",
        level: recipe.level,
      });
    } catch (e) {
      e.fixtureCase = recipe.name;
      throw e;
    } finally {
      if (s) await s.dispose();
    }
  }
  return results;
}
