import test from "node:test";
import { gunzipSync } from "node:zlib";
import { createHash } from "node:crypto";
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

// Frozen unchanged physical ingress from author88d final remote-{go,node,deno}.log.
// Compressed solely to keep these OFFLINE verifier regressions within the allocated
// test file. Hashes cover decoded evidence bytes; these tests launch no process.
const capturedEvidence = [
  {
    "sha256": "dbf47fb6033729b1c3ebfaf6716d32ee30e65c173775a6d9d4153e6d2abc9778",
    "gzip_base64": "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"
  },
  {
    "sha256": "6c4ef089f424e7ef2d3eea708a8c3c8ae360f2ca12d583a99a0347607d306c06",
    "gzip_base64": "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"
  },
  {
    "sha256": "0b351fd089feeeda4f3832f0c69b9de72dc721aff80e35115c5452d4c96bbe38",
    "gzip_base64": "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"
  },
  {
    "sha256": "15b39cddadc1750931eb9114494cac056add949cbe61d340dfbb96453668d91a",
    "gzip_base64": "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"
  },
  {
    "sha256": "1aa95a360117ca70077efcdba36de7add1dd6600b76606d408eecf84264cc656",
    "gzip_base64": "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"
  },
  {
    "sha256": "06dbfd48523f92715acd5d7f33484a707c8614c3edeaa10e9dd7a35133488e05",
    "gzip_base64": "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"
  }
];
function capturedPositives() {
  return capturedEvidence.map(({ sha256, gzip_base64 }) => {
    const bytes = gunzipSync(Buffer.from(gzip_base64, "base64"));
    assert.equal(createHash("sha256").update(bytes).digest("hex"), sha256);
    return JSON.parse(bytes.toString("utf8"));
  });
}
// Match the independent original witness construction: an owned physical
// snapshot is inserted before a later good snapshot, with all control IDs joined.
function intermediate(original, effects, afterFirst = false) {
  const e = structuredClone(original), records = e.records;
  const indices = records.flatMap((r, i) => r.kind === "control" && JSON.parse(r.raw).gate === "snapshot" ? [i] : []);
  const i = indices[Number(afterFirst)], c = JSON.parse(records[i].raw);
  const snapshot = records.slice(i).find(r => r.kind === "event" && JSON.parse(r.raw).fixture_event.kind === "snapshot");
  const value = structuredClone(JSON.parse(snapshot.raw).fixture_event.effects);
  effects(value);
  const inserted = [
    { kind: "control", ms: records[i].ms, raw: JSON.stringify(c) + "\n" },
    { kind: "event", ms: records[i].ms, raw: JSON.stringify({ fixture_event: { kind: "snapshot", effects: value } }) + "\n" },
    { kind: "event", ms: records[i].ms, raw: JSON.stringify({ fixture_event: { kind: "control_received", seq: c.seq } }) + "\n" },
  ];
  for (const r of records.slice(i)) {
    if (r.kind === "control") { const v = JSON.parse(r.raw); v.seq++; r.raw = JSON.stringify(v) + "\n"; }
    if (r.kind === "event") {
      const v = JSON.parse(r.raw);
      if (v.fixture_event.kind === "control_received") { v.fixture_event.seq++; r.raw = JSON.stringify(v) + "\n"; }
    }
  }
  records.splice(i, 0, ...inserted); records.forEach((r, j) => { r.seq = j + 1; });
  return e;
}

test("offline unchanged captured positives and legitimate transitional physical snapshots", () => {
  for (const original of capturedPositives()) {
    assert.equal(verifyRemoteEvidence(original), true);
    // Queue/reservation movement is not cumulative and need not equal the later
    // saturated barrier. Keep author callback counters unchanged and in-domain.
    const transitional = intermediate(original, v => {
      v.control_queued = 1; v.reserved_frames = 1; v.reserved_bytes = 1024;
    }, true);
    assert.equal(verifyRemoteEvidence(transitional), true);
  }
});

test("offline every intermediate physical snapshot refuses adverse effects before replacement", () => {
  const original = capturedPositives().find(e => e.runtime === "go" && e.scenario === "sdk-lifecycle-overflow-v2");
  assert.equal(verifyRemoteEvidence(original), true);
  // These first three reproduce the independent original constructed witnesses.
  const mutations = [
    v => { v.unload_attempts = 1; }, v => { v.get = 1; }, v => { v.commits = 1; },
    v => { v.entered = "1"; }, v => { v.load = 0; },
    v => { v.load = -1; }, v => { v.entered = 1.5; }, v => { v.entered = Number.MAX_SAFE_INTEGER + 1; },
    v => { v.unload_attempts = null; },
    v => { v.future_effect = 0; }, v => { v.reserved_frames = 3; v.reserved_bytes = 3072; },
    v => { v.reserved_frames = 1; v.reserved_bytes = 2048; },
    v => { v.control_queued = null; }, v => { delete v.reserved_bytes; },
  ];
  for (const mutate of mutations) {
    assert.throws(() => verifyRemoteEvidence(intermediate(original, mutate)), mutate.toString());
  }
  const forward = capturedPositives().find(e => e.runtime === "go" && e.scenario === "sdk-forward-overflow-v2");
  assert.throws(() => verifyRemoteEvidence(intermediate(forward, v => {
    v.entered = 16; v.hold = 15;
  }, 2)), /decreasing cumulative/);
  const returned = intermediate(forward, v => { v.returned = 16; }, 3);
  assert.throws(() => verifyRemoteEvidence(intermediate(returned, v => { v.returned = 15; }, 4)), /decreasing cumulative/);
});
