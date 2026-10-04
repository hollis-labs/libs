import { test } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { ChildSession } from "./child-parent.js";
import { transcriptMetadata } from "./corpus-metadata.js";
const worker = fileURLToPath(new URL("./duplex-worker.js", import.meta.url));
const fixtureLimits = {
  frame: 256,
  diagnostics: 512,
  event: 256,
  stderrTotal: 8192,
  step: 2000,
  watchdog: 5000,
  grace: 20,
  reap: 1000,
};
async function run(profile, action) {
  const session = await ChildSession.start(
    process.execPath,
    [worker, profile],
    { limits: fixtureLimits },
  );
  try {
    await action(session);
  } finally {
    await session.dispose();
    assert.ok(session.exit, "child not reaped");
  }
}
test("private inherited control pipe releases and reaps the Node fixture", async () =>
  run("control-only", async (s) => {
    await s.event((e) => e.kind === "ready");
    await s.release("feasibility");
    assert.deepEqual(await s.wait(), { code: 0, signal: null });
  }));
test("hung child escalates to SIGKILL and is joined", async () =>
  run("fault:hung", async (s) => {
    await s.event((e) => e.kind === "ready");
    await assert.rejects(
      s.bounded(s.frames.take(), "step_watchdog", 30),
      (e) => e.code === "step_watchdog",
    );
    await s.terminate();
    assert.equal(s.exit.signal, "SIGKILL");
  }));
test("early child exit cannot masquerade as expected protocol output", async () =>
  run("fault:early", async (s) => {
    await assert.rejects(s.frame(), (e) => e.code === "unexpected_eof");
    assert.equal((await s.wait()).code, 23);
  }));
test("oversized stdout is rejected without an unbounded line drain", async () =>
  run("fault:oversized", async (s) => {
    await assert.rejects(s.frame(), (e) => e.code === "parent_frame_limit");
  }));
test("EOF fragment is a truncated stdout transport", async () =>
  run("fault:no-lf", async (s) => {
    await assert.rejects(s.frame(), (e) => e.code === "truncated_stdout");
  }));
test("stderr flood aborts while retaining only bounded diagnostics", async () =>
  run("fault:stderr", async (s) => {
    await assert.rejects(s.frame(), (e) => e.code === "stderr_limit");
    assert.ok(s.diagnostics.length <= fixtureLimits.diagnostics);
  }));
test("blocked parent stdin write times out and reaps its child", async () =>
  run("fault:blocked", async (s) => {
    await s.event((e) => e.kind === "ready");
    s.limits.step = 30;
    await assert.rejects(
      s.send("x".repeat(1048576)),
      (e) => e.code === "step_watchdog",
    );
  }));
test("absent fixture barrier is an explicit watchdog failure", async () =>
  run("fault:missing", async (s) => {
    await s.event((e) => e.kind === "ready");
    await assert.rejects(
      s.bounded(
        s.events.take((e) => e.kind === "missing"),
        "step_watchdog",
        30,
      ),
      (e) => e.code === "step_watchdog",
    );
  }));
test("spawn failure is observed and child handles are closed", async () => {
  const s = await ChildSession.start("/nonexistent-fixture-worker", []);
  try {
    await assert.rejects(s.frame(), (e) => e.code === "spawn_failed");
  } finally {
    await s.dispose();
    assert.ok(s.exit);
  }
});
test("metadata validates levels and owners before proposed availability", () => {
  for (const f of [
    {},
    { level: "normative", steps: [], status: "unknown" },
    {
      level: "normative",
      steps: [],
      status: "proposed",
      finding: "not implemented",
    },
    { level: "normative", steps: [{ level: "other" }] },
    { level: "normative", steps: [{ level: "observed-quirk" }] },
  ])
    assert.throws(() => transcriptMetadata(f));
  assert.deepEqual(
    transcriptMetadata({
      level: "normative",
      steps: [{ level: "observed-quirk", preferred: "reject" }],
    }),
    ["observed-quirk"],
  );
});

test("parent abort terminates and reaps an active child", async () => {
  const controller = new AbortController();
  const s = await ChildSession.start(process.execPath, [worker, "fault:hung"], {
    limits: fixtureLimits,
    signal: controller.signal,
  });
  try {
    await s.event((e) => e.kind === "ready");
    controller.abort();
    await assert.rejects(s.frame(), (e) => e.code === "parent_aborted");
  } finally {
    await s.dispose();
    assert.ok(s.exit);
  }
});
test("parent rejects a second live child and widened safety ceilings", async () => {
  await assert.rejects(
    ChildSession.start(process.execPath, [], {
      limits: { frame: 16 * 1024 * 1024 },
    }),
    (e) => e.code === "invalid_parent_limits",
  );
  await run("fault:missing", async (s) => {
    await s.event((e) => e.kind === "ready");
    await assert.rejects(
      ChildSession.start(process.execPath, [worker, "control-only"]),
      (e) => e.code === "parent_child_limit",
    );
  });
});
