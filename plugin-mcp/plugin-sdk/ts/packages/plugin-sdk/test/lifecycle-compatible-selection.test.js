import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { selectLifecycleCases, LIFECYCLE_MANIFEST_BYTES } from "./lifecycle-compatible-selection.js";
import { verifyRemoteEvidence, remoteDisposition } from "./lifecycle-compatible-replay.js";

const raw = await readFile(new URL("../../../../protocol/v2/fixtures/duplex-child-lifecycle-compatible-v2.json", import.meta.url), "utf8");
const fixture = JSON.parse(raw), host = "public-conn-isolated-transport", sdk = "sdk-normal-serve-remote-overflow";
const clone = () => structuredClone(fixture);
const select = (m, mode = host, names) => selectLifecycleCases(JSON.stringify(m), mode, names);

test("generic ordered metadata, statuses, nil-all/empty-none and two isolated domains", () => {
  for (const [mode, group] of [[host, "lifecycle_compatible_v2"], [sdk, "sdk_remote_overflow_v2"]]) {
    assert.deepEqual(select(fixture, mode), fixture.groups[group]);
    assert.deepEqual(select(fixture, mode, []), []);
    assert.deepEqual(select(fixture, mode, fixture.groups[group].map(r => r.name).reverse()), fixture.groups[group]);
    assert.throws(() => select(fixture, mode, [fixture.groups[group === "lifecycle_compatible_v2" ? "sdk_remote_overflow_v2" : "lifecycle_compatible_v2"][0].name]));
  }
  const m = clone();
  m.groups.lifecycle_compatible_v2.push({ name: "new-case", scenario: "future-handler", profile: "future-profile",
    level: "normative", status: "observed", owner: "\u00a0", reason: "Accountable future metadata." });
  assert.equal(select(m).at(-1).name, "new-case"); // No case membership allowlist.
  assert.equal(select(m).at(-1).status, "observed");
  assert.equal(remoteDisposition(select(m).at(-1), true), "unavailable");
  assert.equal(remoteDisposition(fixture.groups.sdk_remote_overflow_v2[0]), "pending");
  assert.equal(remoteDisposition(fixture.groups.sdk_remote_overflow_v2[0], true), "candidate");
  assert.deepEqual(select(m, host)[0], fixture.groups.lifecycle_compatible_v2[0]);
});

test("raw token loss, UTF-8, BOM, depth, Unicode and canonical versions refuse", () => {
  const bad = [
    Buffer.from([0xff]), Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(raw)]),
    raw + " false", raw.replace('"corpus_version": 1', '"corpus_version": 1.0'),
    raw.replace('"corpus_version": 1', '"corpus_version": 1e0'),
    raw.replace('"contract_version": 2', '"contract_version": "2"'),
    raw.replace('"version": 1', '"version": 1.0'),
    raw.replace('"corpus_version": 1', '"corpus_version":1,"corpus_version":1'),
    raw.replace('"owner": "/root/sdk_owner_replacement"', '"owner":"/root/sdk_owner_replacement","\\u006fwner":"/root/sdk_owner_replacement"'),
    raw.replace('"owner": "/root/sdk_owner_replacement"', '"owner":"\\ud800"'),
    raw.replace('"reason":', '"reason":"okay","extra":' + "[".repeat(130) + "0" + "]".repeat(130) + ',"reason":'),
    raw + " ".repeat(LIFECYCLE_MANIFEST_BYTES), "\ud800", JSON.parse(raw),
  ];
  for (const value of bad) assert.throws(() => selectLifecycleCases(value, host));
  assert.deepEqual(selectLifecycleCases(Buffer.from(raw), sdk), fixture.groups.sdk_remote_overflow_v2);
});

test("closed objects, all groups, modes, statuses, exclusions and subsets validate before filtering", () => {
  const mutations = [
    m => { delete m.corpus_version; }, m => { m.contract_version = null; },
    m => { m.extra = {}; }, m => { m.selection.kind = "union"; },
    m => { m.selection.extra = []; }, m => { delete m.selection.modes[sdk]; },
    m => { m.selection.modes.future = m.selection.modes[sdk]; },
    m => { m.selection.modes[host].source_group = "sdk_remote_overflow_v2"; },
    m => { m.selection.modes[sdk].source_group = "missing"; },
    m => { m.selection.modes[sdk].include_cases = []; },
    m => { m.selection.modes[sdk].exclude_scenarios = null; },
    m => { m.selection.modes[sdk].exclude_scenarios = ["forward-v2"]; },
    m => { m.selection.modes[sdk].exclude_scenarios = ["missing"]; },
    m => { m.selection.modes[sdk].exclude_scenarios = ["sdk-forward-overflow-v2", "sdk-forward-overflow-v2"]; },
    m => { m.groups.extra = []; }, m => { delete m.groups.sdk_remote_overflow_v2; },
    m => { m.groups.sdk_remote_overflow_v2 = []; }, m => { m.groups.sdk_remote_overflow_v2 = {}; },
    m => { m.groups.sdk_remote_overflow_v2[1] = null; },
    m => { m.groups.sdk_remote_overflow_v2[1].status = "passed"; },
    m => { delete m.groups.sdk_remote_overflow_v2[1].status; },
    m => { m.groups.sdk_remote_overflow_v2[1].status = null; },
    m => { m.groups.sdk_remote_overflow_v2[1].owner = " \t\n"; },
    m => { delete m.groups.sdk_remote_overflow_v2[1].reason; },
    m => { m.groups.sdk_remote_overflow_v2[1].reason = 9; },
    m => { m.groups.sdk_remote_overflow_v2[1].profile = "Expanded"; },
    m => { m.groups.sdk_remote_overflow_v2[1].profile = "expanded\n"; },
    m => { m.groups.sdk_remote_overflow_v2[1].Name = "alternate"; },
    m => { m.groups.sdk_remote_overflow_v2[1].name = m.groups.lifecycle_compatible_v2[0].name; },
    m => { m.groups.sdk_remote_overflow_v2[1].scenario = m.groups.sdk_remote_overflow_v2[0].scenario; },
    m => { m.groups.sdk_remote_overflow_v2[1].level = "quirk"; },
  ];
  for (const mutate of mutations) {
    const m = clone(); mutate(m);
    assert.throws(() => select(m, host, []), mutate.toString());
  }
  const m = clone(), first = m.groups.lifecycle_compatible_v2[0];
  m.selection.modes[host].exclude_scenarios = [first.scenario];
  assert.deepEqual(select(m), [m.groups.lifecycle_compatible_v2[1]]);
  assert.throws(() => select(m, host, [first.name]));
  delete first.owner;
  assert.throws(() => select(m, host, [])); // Excluded proposed row still validates.
  for (const mode of [undefined, null, "missing"]) assert.throws(() => selectLifecycleCases(raw, mode));
  for (const names of [null, "all", ["missing"], [fixture.groups.lifecycle_compatible_v2[0].name,
    fixture.groups.lifecycle_compatible_v2[0].name]]) assert.throws(() => select(fixture, host, names));
});

// SYNTHETIC reached guards, separate from OS child evidence. The transcript uses
// real contract DTOs but makes no runtime/custody claim of its own.
function synthetic(scenario) {
  const forward = scenario === "sdk-forward-overflow-v2", n = forward ? 16 : 2;
  const records = [], add = (kind, rest = {}) => records.push({ seq: records.length + 1, ms: records.length + 1, kind, ...rest });
  let control = 0;
  const input = (id, method, params) => {
    const raw = JSON.stringify({ jsonrpc: "2.0", id, method, params }) + "\n";
    add("input", { raw }); add("input_receipt", { id, raw, bytes: Buffer.byteLength(raw), complete: true });
  };
  const event = e => add("event", { raw: JSON.stringify({ fixture_event: e }) + "\n" });
  const output = (id, result) => add("stdout", { raw: JSON.stringify({ jsonrpc: "2.0", id, result }) + "\n" });
  const release = gate => {
    add("control", { raw: JSON.stringify({ seq: ++control, op: "release", gate }) + "\n" });
  };
  const ack = () => event({ kind: "control_received", seq: control });
  const values = phase => ({ entered: phase === "startup" ? 1 : forward ? 17 : 3,
    load: phase === "startup" || forward ? 1 : 3, ...(forward && phase !== "startup" ? { hold: 16 } : {}),
    ...(phase === "final" && forward ? { returned: 16 } : {}),
    reverse_pending: 0, ordinary_queued: 0, control_queued: 0,
    reserved_frames: phase === "saturated" ? n : 0, reserved_bytes: phase === "saturated" ? n * 1024 : 0 });
  const barrier = (phase, valuePhase = phase) => {
    const effects = values(valuePhase);
    release("snapshot"); event({ kind: "snapshot", effects }); ack(); add("barrier", { phase, effects: structuredClone(effects) });
  };
  event({ kind: "ready" });
  const init = { plugin_dir: "/fixture", data_dir: "/fixture/data", cache_dir: "/fixture/cache", config: {},
    log_level: "info", host_info: { version: "fixture", protocol: 2 }, capability_contract: 1,
    incarnation: { host_instance: "fixture-host", owner_id: "fixture", owner_generation: 1 }, grants: [],
    host_services: structuredClone(storageTemplate.host_services), context: { timeout_ms: 9000 } };
  // The independent immutable offer is loaded below; no plugin-host authority.
  input(1, "plugin/init", init); event({ kind: "init_client", client: false, id: 1 });
  output(1, { id: "fixture", name: "Fixture", description: "conformance", version: "1.0.0", protocol: 2, capability_contract: 1, reverse_rpc_version: 1 });
  input(2, "plugin/load", { context: { timeout_ms: 9000 } });
  event({ kind: "entered", id: 2, name: "load", deadline: true });
  release("request-2"); event({ kind: "returned", id: 2 }); ack(); output(2, {}); barrier("startup");
  for (let i = 3; i < 3 + n; i++) {
    input(i, forward ? "command/execute" : "plugin/load", forward ? { name: "hold", args: "{}", session_id: "" } :
      { context: { timeout_ms: 9000 } });
    event({ kind: "entered", id: i, name: forward ? "hold" : "load", deadline: !forward });
  }
  barrier("saturated");
  input(n + 3, forward ? "command/execute" : "plugin/load", forward ? { name: "hold", args: "{}", session_id: "" } :
    { context: { timeout_ms: 9000 } });
  add("stdout", { raw: JSON.stringify({ jsonrpc: "2.0", id: n + 3, error: { code: -32010, message: "rate_limited",
    data: { contract: "host-rpc/1", code: "rate_limited", request_id: n + 3, effect_state: "not_started", retryable: false } } }) + "\n" });
  barrier("after-refusal", "saturated");
  for (let i = 3; i < 3 + n; i++) {
    release("request-" + i); event({ kind: "returned", id: i }); ack(); output(i, forward ? { action: "noop" } : {});
  }
  barrier("final"); add("stdin_eof"); event({ kind: "unload_started" });
  event({ kind: "finished", effects: { ...values("final"), unload_attempts: 1 }, transport_error: null });
  add("stdout_eof"); add("stderr_eof"); add("control_close");
  add("process_close", { exit: { code: 0, signal: null } });
  return { domain: "sdk-only-fake-host-remote-overflow", scenario, runtime: "go", authority_ms: 10000,
    records, exit: { code: 0, signal: null }, fallback: false, mailboxes_empty: true };
}
const storageTemplate = JSON.parse(await readFile(new URL("../../../../protocol/v2/fixtures/host-storage.json", import.meta.url), "utf8")).init;

test("synthetic valid domain counters and strict custody/EOF guards", () => {
  for (const scenario of ["sdk-forward-overflow-v2", "sdk-lifecycle-overflow-v2"]) {
    const positive = synthetic(scenario);
    assert.equal(verifyRemoteEvidence(positive), true);
    const mutations = [
      e => { e.exit.code = 1; }, e => { e.fallback = true; },
      e => { e.mailboxes_empty = false; },
      e => { e.records.find(r => r.kind === "barrier" && r.phase === "final").effects.reserved_frames = 1; },
      e => { e.records.find(r => r.kind === "barrier" && r.phase === "saturated").effects.entered = 19; },
      e => { e.records.find(r => r.kind === "input_receipt").complete = false; },
      e => { e.records.find(r => r.kind === "input_receipt").bytes = 1; },
      e => { e.records.find(r => r.kind === "stdout" && r.raw.includes("rate_limited")).raw =
        e.records.find(r => r.kind === "stdout" && r.raw.includes("rate_limited")).raw.replace('"retryable":false', '"retryable":true'); },
      e => { e.records.find(r => r.kind === "stdin_eof").kind = "signal"; },
      e => { e.records.find(r => r.kind === "stderr_eof").kind = "unknown"; },
      e => { e.records = e.records.filter(r => r.kind !== "stderr_eof");
        e.records.forEach((r, index) => { r.seq = index + 1; }); },
      e => { e.records.at(-1).exit.signal = "SIGTERM"; },
      e => { e.records[0].ms = 10001; },
      e => { e.records.find(r => r.kind === "control").raw =
        e.records.find(r => r.kind === "control").raw.replace("request-2", "request-999"); },
      e => { e.records.find(r => r.kind === "input").raw =
        e.records.find(r => r.kind === "input").raw.replace('"timeout_ms":9000', '"timeout_ms":10001'); },
      e => { e.records.find(r => r.kind === "stdout" && r.raw.includes("rate_limited")).raw =
        e.records.find(r => r.kind === "stdout" && r.raw.includes("rate_limited")).raw.replace('"request_id":', '"request_owner":"foreign","request_id":'); },
      e => { e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"entered"')).raw =
        e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"entered"')).raw.replace('"id":2', '"id":999'); },
      e => { e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"control_received"')).raw =
        e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"control_received"')).raw.replace('"seq":1', '"seq":2'); },
      e => { e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"finished"')).raw =
        e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"finished"')).raw.replace('"transport_error":null', '"transport_error":"late_failure"'); },
      e => { e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"finished"')).raw =
        e.records.find(r => r.kind === "event" && r.raw.includes('"kind":"finished"')).raw.replace('"unload_attempts":1', '"unload_attempts":2'); },
    ];
    for (const mutate of mutations) {
      const evidence = structuredClone(positive); mutate(evidence);
      assert.throws(() => verifyRemoteEvidence(evidence), mutate.toString());
    }
  }
});

test("synthetic forged snapshot cannot be laundered by an invented barrier", () => {
  const evidence = synthetic("sdk-forward-overflow-v2");
  const snapshot = evidence.records.filter(r => r.kind === "event" && r.raw.includes('"kind":"snapshot"')).at(-1);
  snapshot.raw = snapshot.raw.replace('"reserved_frames":0', '"reserved_frames":1');
  assert.throws(() => verifyRemoteEvidence(evidence), "barrier must match physical snapshot");
});

test("synthetic pre-EOF cleanup counter refuses even with otherwise valid event inventory", () => {
  const evidence = synthetic("sdk-lifecycle-overflow-v2");
  const snapshot = evidence.records.filter(r => r.kind === "event" && r.raw.includes('"kind":"snapshot"')).at(-1);
  const wrapper = JSON.parse(snapshot.raw);
  wrapper.fixture_event.effects.unload_attempts = 1;
  snapshot.raw = JSON.stringify(wrapper) + "\n";
  evidence.records.find(r => r.kind === "barrier" && r.phase === "final").effects.unload_attempts = 1;
  assert.throws(() => verifyRemoteEvidence(evidence), /cleanup before actual EOF/);
});
