// SDK-only fake-host candidate validation; never public plugin-host Conn evidence.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import { ChildSession } from "./child-parent.js";
import { childSpec } from "./child-spec.js";
import { selectLifecycleCases } from "./lifecycle-compatible-selection.js";
import { validateJSON, decodeJSONObject } from "../dist/strict-json.js";
import { decodeInitParams, decodeInitResult, validateInitResult } from "../dist/init-contract.js";
import { validateRuntimeResult } from "../dist/payload.js";

export const REMOTE_MODE = "sdk-normal-serve-remote-overflow";
const ASSET = new URL("../../../../protocol/v2/fixtures/duplex-child-lifecycle-compatible-v2.json", import.meta.url);
const TEMPLATE = new URL("../../../../protocol/v2/fixtures/host-storage.json", import.meta.url);
const sha = bytes => createHash("sha256").update(bytes).digest("hex");
const closed = (value, fields, nullable = []) => decodeJSONObject(JSON.stringify(value), fields, [], nullable);
function parsed(raw) { validateJSON(raw); return JSON.parse(raw); }
function budget(deadline) {
  const remaining = Math.floor(deadline - performance.now());
  assert.ok(remaining > 0 && remaining <= 10000, "continuous authority deadline expired");
  return { timeout_ms: remaining };
}
function expected(scenario) {
  if (scenario === "sdk-forward-overflow-v2")
    return { slots: 16, method: "command/execute", name: "hold", entered: 17, load: 1, returned: 16 };
  if (scenario === "sdk-lifecycle-overflow-v2")
    return { slots: 2, method: "plugin/load", name: "load", entered: 3, load: 3, returned: 0 };
  return undefined;
}
export function remoteDisposition(row, candidateValidation = false) {
  if (row.profile !== "expanded" || !expected(row.scenario)) return "unavailable";
  if (row.status === "proposed" && !candidateValidation) return "pending";
  assert.ok(row.status === "proposed" || row.status === "observed", "unsupported authored status");
  return "candidate";
}
function counters(v, exp, phase) {
  const held = phase === "saturated" || phase === "after-refusal";
  const allowed = ["entered", "load", "hold", "returned", "reverse_pending", "ordinary_queued",
    "control_queued", "reserved_frames", "reserved_bytes", "unload_attempts"];
  assert.ok(Object.keys(v).every(key => allowed.includes(key)), "unexpected callback/effect counter");
  assert.equal(v.entered, phase === "startup" ? 1 : exp.entered);
  assert.equal(v.load, phase === "startup" ? 1 : exp.load);
  assert.equal(v.hold ?? 0, exp.name === "hold" && phase !== "startup" ? 16 : 0);
  assert.equal(v.get ?? 0, 0); assert.equal(v.put ?? 0, 0); assert.equal(v.commits ?? 0, 0);
  assert.equal(v.returned ?? 0, phase === "final" ? exp.returned : 0);
  assert.equal(v.reverse_pending, 0);
  assert.equal(v.ordinary_queued, 0); assert.equal(v.control_queued, 0);
  assert.equal(v.reserved_frames, held ? exp.slots : 0);
  assert.equal(v.reserved_bytes, held ? exp.slots * 1024 : 0);
}

// Re-check the complete physical ingress ledger, including frames arriving after
// the named response/finished expectations. Synthetic guards call this same door.
export function verifyRemoteEvidence(evidence) {
  closed(evidence, ["domain", "scenario", "runtime", "authority_ms", "records", "exit", "fallback", "mailboxes_empty"]);
  assert.equal(evidence.domain, "sdk-only-fake-host-remote-overflow");
  const exp = expected(evidence.scenario);
  assert.ok(exp, "unsupported remote scenario");
  assert.ok(["go", "node", "deno"].includes(evidence.runtime));
  assert.ok(evidence.authority_ms > 0 && evidence.authority_ms <= 10000);
  assert.equal(evidence.fallback, false); assert.equal(evidence.mailboxes_empty, true);
  assert.deepEqual(evidence.exit, { code: 0, signal: null });
  assert.ok(Array.isArray(evidence.records) && evidence.records.length <= 256);
  const requests = new Map(), results = new Map(), entries = new Map(), returns = new Set();
  const controls = new Map(), acks = new Set(), aborted = new Set(), receipts = new Set();
  let prior = 0, previousMS = -1, high = 0, ready = false, initClient = false, cleanup = false, finished = false;
  let startup = false, saturation = false, afterRefusal = false, final = false, halfclose = false;
  let stdoutEOF = false, stderrEOF = false, processClose = false, controlClose = false;
  let overflowID, snapshotControls = 0, lastSnapshot;
  const enteredAtStartup = new Set();
  for (const record of evidence.records) {
    assert.ok(record && typeof record === "object" && !Array.isArray(record));
    assert.equal(record.seq, ++prior, "physical ingress sequence");
    assert.ok(Number.isFinite(record.ms) && record.ms >= 0 && record.ms < evidence.authority_ms, "expired evidence");
    assert.ok(record.ms >= previousMS, "reordered ingress clock"); previousMS = record.ms;
    if (record.kind === "input") {
      assert.ok(!halfclose && !finished, "input after EOF");
      const frame = parsed(record.raw);
      closed(frame, ["jsonrpc", "id", "method", "params"]);
      assert.equal(frame.jsonrpc, "2.0");
      assert.ok(Number.isSafeInteger(frame.id) && frame.id > high, "fresh safe monotonic parent ID");
      high = frame.id; assert.ok(!requests.has(frame.id));
      if (requests.size === 0) {
        assert.equal(frame.method, "plugin/init");
        const init = decodeInitParams(JSON.stringify(frame.params));
        assert.deepEqual(init.grants, []);
        closed(init.context, ["timeout_ms"]);
      } else if (requests.size === 1) {
        assert.equal(frame.method, "plugin/load");
      } else {
        assert.ok(startup, "saturation before physical startup retirement");
        assert.equal(frame.method, exp.method);
        if (requests.size === exp.slots + 2) {
          assert.ok(saturation, "overflow before real holder occupancy");
          overflowID = frame.id;
        } else assert.ok(requests.size < exp.slots + 2, "extra input");
      }
      if (frame.method === "plugin/init" || frame.method === "plugin/load") {
        if (frame.method === "plugin/load") closed(frame.params, ["context"]);
        closed(frame.params.context, ["timeout_ms"]);
        const ms = frame.params.context.timeout_ms;
        assert.ok(Number.isInteger(ms) && ms > 0 && ms <= evidence.authority_ms - Math.floor(record.ms),
          "renewed/unbounded/delegated lifecycle context");
      } else {
        closed(frame.params, ["name", "args", "session_id"]);
        assert.equal(frame.params.name, "hold"); assert.equal(frame.params.args, "{}");
        assert.equal(frame.params.session_id, "");
      }
      requests.set(frame.id, frame);
    } else if (record.kind === "input_receipt") {
      assert.ok(requests.has(record.id) && !receipts.has(record.id));
      assert.equal(record.bytes, Buffer.byteLength(record.raw));
      assert.equal(record.raw, JSON.stringify(requests.get(record.id)) + "\n");
      assert.equal(record.complete, true); receipts.add(record.id);
    } else if (record.kind === "control") {
      assert.ok(!halfclose && !finished, "control after halfclose");
      const c = parsed(record.raw);
      closed(c, ["seq", "op", "gate"]);
      assert.equal(c.seq, controls.size + 1); assert.ok(c.seq <= 64);
      assert.equal(c.op, "release");
      if (c.gate === "snapshot") snapshotControls++;
      else {
        assert.ok(/^request-[1-9][0-9]*$/.test(c.gate));
        const id = Number(c.gate.slice(8));
        assert.ok(entries.has(id) && id !== overflowID && !returns.has(id), "unowned/duplicate gate");
        assert.ok(![...controls.values()].some(v => v.gate === c.gate), "duplicate release");
      }
      controls.set(c.seq, c);
    } else if (record.kind === "stdout") {
      const frame = parsed(record.raw);
      assert.ok(!halfclose, "physical result after halfclose");
      assert.equal(frame.jsonrpc, "2.0");
      assert.ok(requests.has(frame.id) && !results.has(frame.id), "foreign/duplicate terminal");
      const request = requests.get(frame.id);
      if (frame.id === overflowID) {
        closed(frame, ["jsonrpc", "id", "error"]); closed(frame.error, ["code", "message", "data"]);
        assert.equal(frame.error.code, -32010); assert.equal(typeof frame.error.message, "string");
        assert.deepEqual(frame.error.data, { contract: "host-rpc/1", code: "rate_limited",
          request_id: frame.id, effect_state: "not_started", retryable: false });
        assert.ok(!entries.has(frame.id), "overflow callback entered");
      } else {
        closed(frame, ["jsonrpc", "id", "result"]);
        if (request.method === "plugin/init") {
          const result = decodeInitResult(JSON.stringify(frame.result));
          validateInitResult(request.params, result);
          assert.equal(result.reverse_rpc_version, 1); assert.equal(result.protocol, 2);
        } else {
          assert.ok([...controls.values()].some(c => c.gate === "request-" + frame.id),
            "response without owned callback release");
          validateRuntimeResult(request.method, frame.result);
          assert.deepEqual(frame.result, request.method === "plugin/load" ? {} : { action: "noop" });
        }
      }
      results.set(frame.id, frame);
    } else if (record.kind === "event") {
      const wrapper = parsed(record.raw); closed(wrapper, ["fixture_event"]);
      const e = wrapper.fixture_event;
      assert.ok(!finished, "event after finished");
      switch (e.kind) {
        case "ready":
          closed(e, ["kind"]); assert.equal(ready, false); ready = true; break;
        case "init_client":
          closed(e, ["kind", "client", ...(Object.hasOwn(e, "id") ? ["id"] : [])]);
          assert.equal(initClient, false); assert.equal(e.client, false); initClient = true; break;
        case "entered": {
          closed(e, ["kind", "id", "name", "deadline"]);
          assert.ok(ready && requests.has(e.id) && e.id !== overflowID && !entries.has(e.id));
          const request = requests.get(e.id);
          assert.equal(e.name, request.method === "plugin/load" ? "load" : "hold");
          assert.equal(e.deadline, request.method === "plugin/load");
          entries.set(e.id, e);
          if (!startup) enteredAtStartup.add(e.id);
          break;
        }
        case "returned":
          closed(e, ["kind", "id"]);
          assert.ok(entries.has(e.id) && !returns.has(e.id));
          assert.ok([...controls.values()].some(c => c.gate === "request-" + e.id), "return without release");
          returns.add(e.id); break;
        case "aborted":
          // TS scope completion aborts its signal AFTER author return. That is
          // actual source behavior, distinct from observer/deadline cancellation.
          closed(e, ["kind", "id", "deadline"]);
          assert.equal(evidence.runtime === "go", false);
          assert.ok(returns.has(e.id) && !aborted.has(e.id)); assert.equal(e.deadline, false);
          aborted.add(e.id); break;
        case "control_received":
          closed(e, ["kind", "seq"]);
          assert.ok(controls.has(e.seq) && !acks.has(e.seq));
          assert.equal(e.seq, acks.size + 1); acks.add(e.seq); break;
        case "snapshot":
          closed(e, ["kind", "effects"]);
          assert.ok(snapshotControls > 0); snapshotControls--; lastSnapshot = e.effects; break;
        case "unload_started":
          closed(e, ["kind"]); assert.ok(halfclose && final && !cleanup);
          cleanup = true; break;
        case "finished":
          closed(e, ["kind", "effects", "transport_error"], ["transport_error"]);
          assert.ok(halfclose && cleanup); assert.equal(e.transport_error, null);
          counters(e.effects, exp, "final"); assert.equal(e.effects.unload_attempts, 1);
          finished = true; break;
        default: assert.fail("unexpected fixture event " + e.kind);
      }
    } else if (record.kind === "barrier") {
      assert.ok(acks.size === controls.size && snapshotControls === 0);
      assert.deepEqual(record.effects, lastSnapshot, "barrier must match physical snapshot");
      if (record.phase === "startup") {
        assert.equal(startup, false); assert.equal(results.size, 2); assert.equal(receipts.size, 2);
        assert.equal(enteredAtStartup.size, 1); assert.equal(returns.size, 1);
        counters(record.effects, exp, "startup"); startup = true;
      } else if (record.phase === "saturated") {
        assert.ok(startup && !saturation); assert.equal(entries.size, exp.slots + 1);
        assert.equal(returns.size, 1); counters(record.effects, exp, "saturated"); saturation = true;
      } else if (record.phase === "after-refusal") {
        assert.ok(saturation && !afterRefusal && results.has(overflowID));
        assert.equal(entries.size, exp.slots + 1); assert.equal(returns.size, 1);
        counters(record.effects, exp, "saturated"); afterRefusal = true;
      } else if (record.phase === "final") {
        assert.ok(afterRefusal && !final);
        assert.equal(results.size, exp.slots + 3); assert.equal(receipts.size, requests.size);
        assert.equal(returns.size, exp.slots + 1); counters(record.effects, exp, "final"); final = true;
      } else assert.fail("unknown custody barrier");
    } else if (record.kind === "stdin_eof") {
      assert.ok(final && !halfclose && acks.size === controls.size); halfclose = true;
    } else if (record.kind === "stdout_eof") {
      assert.ok(halfclose && !stdoutEOF); stdoutEOF = true;
    } else if (record.kind === "stderr_eof") {
      assert.ok(finished && !stderrEOF); stderrEOF = true;
    } else if (record.kind === "control_close") {
      assert.ok(halfclose && !controlClose); controlClose = true;
    } else if (record.kind === "process_close") {
      assert.ok(finished && stdoutEOF && stderrEOF && !processClose);
      assert.deepEqual(record.exit, { code: 0, signal: null }); processClose = true;
    } else assert.fail("unknown physical ledger record");
  }
  assert.ok(ready && initClient && startup && saturation && afterRefusal && final && halfclose &&
    cleanup && finished && stdoutEOF && stderrEOF && processClose);
  assert.equal(controlClose, evidence.runtime !== "deno");
  assert.equal(results.size, requests.size); assert.equal(receipts.size, requests.size);
  assert.equal(acks.size, controls.size); assert.equal(snapshotControls, 0);
  assert.equal(entries.size, exp.slots + 1);
  return true;
}

function observe(s, origin, deadline) {
  const records = [], listeners = [], eof = new Map();
  let failure;
  const add = (kind, fields = {}) => {
    try {
      assert.ok(records.length < 256, "physical ledger limit");
      assert.ok(performance.now() < deadline, "continuous authority deadline expired");
      records.push({ seq: records.length + 1, ms: performance.now() - origin, kind, ...fields });
    } catch (error) { failure ??= error; }
  };
  const on = (target, name, listener) => {
    target.on(name, listener); listeners.push(() => target.off(name, listener));
  };
  // Synchronous installation before ready/Init/any caller yield.
  for (const [name, stream, cap] of [["stdout", s.child.stdout, 65536], ["stderr", s.child.stderr, 4096]]) {
    let pending = Buffer.alloc(0);
    const receipt = {};
    receipt.promise = new Promise(resolve => { receipt.resolve = resolve; }); eof.set(name, receipt);
    on(stream, "data", bytes => {
      try {
        for (let at = 0; at < bytes.length;) {
          const lf = bytes.indexOf(10, at), end = lf < 0 ? bytes.length : lf + 1;
          const part = bytes.subarray(at, end);
          assert.ok(pending.length + part.length <= cap, "retained physical frame limit");
          pending = Buffer.concat([pending, part]);
          if (lf >= 0) {
            const raw = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(pending);
            assert.ok(raw.charCodeAt(0) !== 0xfeff, "physical BOM");
            validateJSON(raw);
            if (name === "stderr") decodeJSONObject(raw, ["fixture_event"]);
            add(name === "stderr" ? "event" : "stdout", { raw });
            pending = Buffer.alloc(0);
          }
          at = end;
        }
      } catch (error) { failure ??= error; }
    });
    on(stream, "end", () => {
      if (pending.length) failure ??= new Error("truncated physical " + name);
      add(name + "_eof"); receipt.resolve();
    });
    on(stream, "error", error => { failure ??= error; receipt.resolve(); });
    on(stream, "close", () => {
      if (!stream.readableEnded) failure ??= new Error("physical " + name + " closed before EOF");
      receipt.resolve();
    });
  }
  if (s.child.stdio[3]) {
    on(s.child.stdio[3], "error", error => { failure ??= error; });
    on(s.child.stdio[3], "close", () => add("control_close"));
  }
  on(s.child, "close", (code, signal) => add("process_close", { exit: { code, signal } }));
  return { records, add, check() { if (failure) throw failure; if (s.failure) throw s.failure; },
    async join() { await Promise.all([...eof.values()].map(v => v.promise)); },
    detach() { for (const remove of listeners) remove(); } };
}

async function runRemote(runtime, goChild, recipe) {
  const exp = expected(recipe.scenario), origin = performance.now(), deadline = origin + 10000;
  const spec = childSpec(runtime, "negotiated:" + recipe.profile, goChild);
  let s, audit;
  try {
    s = await ChildSession.start(spec.command, spec.args, spec.options);
    audit = observe(s, origin, deadline); // No intervening await after child creation.
    const step = async promise => {
      audit.check(); budget(deadline);
      const result = await s.bounded(promise, "continuous_authority_deadline", Math.min(5000, deadline - performance.now()));
      audit.check(); budget(deadline); return result;
    };
    const event = predicate => step(s.event(predicate));
    const response = id => step(s.frame(v => v.id === id && !v.method));
    let id = 0, controlSeq = 0;
    const send = async (method, params) => {
      const current = ++id, raw = JSON.stringify({ jsonrpc: "2.0", id: current, method, params }) + "\n";
      audit.add("input", { raw });
      await step(s.write(Buffer.from(raw)));
      audit.add("input_receipt", { id: current, raw, bytes: Buffer.byteLength(raw), complete: true });
      return current;
    };
    const release = async gate => {
      assert.ok(controlSeq < 64);
      audit.add("control", { raw: JSON.stringify({ seq: ++controlSeq, op: "release", gate }) + "\n" });
      await step(s.release(gate));
    };
    const snapshot = async (phase, test) => {
      for (let n = 0; n < 40; n++) {
        await release("snapshot");
        const value = (await event(e => e.kind === "snapshot")).effects;
        if (test(value)) { counters(value, exp, phase); audit.add("barrier", { phase, effects: value }); return value; }
        await step(delay(5));
      }
      assert.fail("custody snapshot barrier not reached");
    };
    await event(e => e.kind === "ready");
    const init = structuredClone(JSON.parse(await readFile(TEMPLATE, "utf8")).init);
    init.grants = []; // No grants/binding/client delegation in this closed driver.
    init.context = budget(deadline);
    const initID = await send("plugin/init", init);
    const initialized = (await response(initID)).value;
    assert.ok(initialized.result); assert.equal(initialized.result.reverse_rpc_version, 1);
    await event(e => e.kind === "init_client");
    const startupID = await send("plugin/load", { context: budget(deadline) });
    await event(e => e.kind === "entered" && e.id === startupID);
    await release("request-" + startupID);
    await event(e => e.kind === "returned" && e.id === startupID);
    assert.deepEqual((await response(startupID)).value.result, {});
    await snapshot("startup", v => v.reserved_frames === 0 && v.control_queued === 0);
    const holders = [];
    for (let n = 0; n < exp.slots; n++) {
      const params = exp.method === "plugin/load" ? { context: budget(deadline) } :
        { name: "hold", args: "{}", session_id: "" };
      const heldID = await send(exp.method, params); holders.push(heldID);
      await event(e => e.kind === "entered" && e.id === heldID);
    }
    await snapshot("saturated", v => v.reserved_frames === exp.slots && v.control_queued === 0);
    const overflowID = await send(exp.method, exp.method === "plugin/load" ?
      { context: budget(deadline) } : { name: "hold", args: "{}", session_id: "" });
    const refusal = (await response(overflowID)).value;
    assert.equal(refusal.error?.data?.code, "rate_limited");
    // Own overflow terminal was physically consumed before holder comparison.
    await snapshot("after-refusal", v => v.reserved_frames === exp.slots && v.control_queued === 0);
    for (const heldID of holders) {
      await release("request-" + heldID);
      await event(e => e.kind === "returned" && e.id === heldID);
      await response(heldID);
    }
    await snapshot("final", v => v.reserved_frames === 0 && v.control_queued === 0);
    audit.add("stdin_eof"); await step(s.end());
    await event(e => e.kind === "unload_started");
    await event(e => e.kind === "finished");
    const exit = await step(s.wait()); await step(audit.join()); audit.check();
    // Scope-completion abort events are consumed and checked by the raw ledger.
    for (const item of [...s.events.items]) {
      assert.equal(item.value.kind, "aborted");
      await event(e => e === item.value);
    }
    assert.equal(s.frames.items.length, 0); assert.equal(s.events.items.length, 0);
    const evidence = { domain: "sdk-only-fake-host-remote-overflow", scenario: recipe.scenario,
      runtime, authority_ms: 10000, records: audit.records, exit, fallback: false, mailboxes_empty: true };
    verifyRemoteEvidence(evidence);
    return { case: recipe.name, authored_status: recipe.status, owner: recipe.owner, reason: recipe.reason,
      mode: REMOTE_MODE, candidate_validation: "passed", evidence_sha256: sha(JSON.stringify(evidence)), evidence };
  } catch (error) {
    error.fixtureCase = recipe.name; throw error;
  } finally {
    // Disposing a verified naturally exited child only removes owned scratch.
    // On failure this helper may terminate; that path never yields passing evidence.
    if (s) await s.dispose();
    audit?.detach();
  }
}

export async function replayLifecycleRemote(runtime, goChild, names, candidateValidation = false) {
  const raw = await readFile(ASSET);
  const selected = selectLifecycleCases(raw, REMOTE_MODE, names);
  const results = [];
  for (const row of selected) {
    const disposition = remoteDisposition(row, candidateValidation);
    if (disposition === "unavailable") {
      results.push({ case: row.name, authored_status: row.status, owner: row.owner, reason: row.reason,
        candidate_validation: "unavailable", reason_execution: "unsupported scenario/profile" });
    } else if (disposition === "pending") {
      results.push({ case: row.name, authored_status: row.status, owner: row.owner, reason: row.reason,
        candidate_validation: "pending", reason_execution: "explicit source-candidate validation required" });
    } else results.push(await runRemote(runtime, goChild, row));
  }
  return { manifest_sha256: sha(raw), mode: REMOTE_MODE, runtime, results };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [runtime, goChild, flag] = process.argv.slice(2);
  try {
    assert.ok(flag === undefined || flag === "--candidate-validation");
    console.log(JSON.stringify(await replayLifecycleRemote(runtime, goChild === "-" ? undefined : goChild,
      undefined, flag === "--candidate-validation")));
  } catch (error) {
    console.error(JSON.stringify({ runtime, case: error.fixtureCase, candidate_validation: "failed",
      failure: error.message, stack: error.stack }));
    process.exitCode = 1;
  }
}
