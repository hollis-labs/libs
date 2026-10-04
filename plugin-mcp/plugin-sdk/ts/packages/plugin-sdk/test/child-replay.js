// Replay entry shared by the Go, Node and Deno child matrix. No SDK exports.
import assert from "node:assert/strict";
import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { initParams } from "./fixtures.js";
import { ChildSession } from "./child-parent.js";
import { transcriptMetadata } from "./corpus-metadata.js";
import { parseJSONTokens, inspectEnvelope } from "../dist/strict-json.js";
import { decodeHostRPCDTO } from "../dist/host-rpc.js";
const directory = new URL(
  "../../../../docs/protocol/v2/transcripts/",
  import.meta.url,
);
const manifest = parseJSONTokens(
  await readFile(
    new URL(
      "../../../../protocol/v2/fixtures/duplex-child.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
assert.equal(manifest.corpus_version, 1);
const worker = fileURLToPath(new URL("./duplex-worker.js", import.meta.url));
export function childSpec(runtime, profile, goChild) {
  if (runtime === "go") {
    assert.ok(goChild, "Go test child path required");
    return {
      command: goChild,
      args: ["-test.run=^TestDuplexFixtureChild$"],
      options: { env: { SDK_FIXTURE_CHILD: profile } },
    };
  }
  if (runtime === "node")
    return { command: process.execPath, args: [worker, profile], options: {} };
  if (runtime === "deno")
    return {
      command: "deno",
      args: [
        "run",
        "--cached-only",
        "--no-npm",
        "--no-check",
        "--allow-env",
        "--fixture-control-permission",
        worker,
        profile,
      ],
      options: { pathControl: true },
    };
  throw new Error("unknown fixture runtime");
}
let activeCase = "control-feasibility";
const parentAbort = new AbortController();
async function session(runtime, profile, goChild) {
  const spec = childSpec(runtime, profile, goChild);
  return ChildSession.start(spec.command, spec.args, {
    ...spec.options,
    signal: parentAbort.signal,
  });
}
function subset(actual, expected) {
  if (expected && typeof expected === "object" && !Array.isArray(expected)) {
    for (const [key, value] of Object.entries(expected)) {
      assert.ok(actual && Object.hasOwn(actual, key), `missing ${key}`);
      subset(actual[key], value);
    }
  } else assert.deepEqual(actual, expected);
}
async function completed(s, effects, success = true) {
  const finished = await s.event((event) => event.kind === "finished");
  if (effects) subset(finished.effects, effects);
  const exit = await s.wait();
  assert.equal(exit.signal, null);
  assert.equal(exit.code, success ? 0 : 1);
  assert.equal(s.frames.items.length, 0, "unexpected late stdout");
  if (s.failure) throw s.failure;
  return finished;
}
export async function replayBase(runtime, goChild, names) {
  names ??= (await readdir(directory))
    .filter((name) => name.endsWith(".json"))
    .sort();
  const results = [];
  for (const name of names) {
    const fixture = parseJSONTokens(
      await readFile(new URL(name, directory), "utf8"),
    );
    const levels = transcriptMetadata(fixture);
    if (
      !manifest.base_profiles.includes(fixture.profile) &&
      fixture.status !== "proposed"
    )
      continue;
    if (fixture.status === "proposed") {
      results.push({
        case: name,
        status: "unavailable",
        owner: fixture.unavailable_owner,
        reason: fixture.finding,
      });
      continue;
    }
    activeCase = name;
    const s = await session(runtime, fixture.profile, goChild);
    s.allowInputClose = fixture.termination === "frame-too-large";
    try {
      for (const step of fixture.steps) {
        try {
          await s.sendStep(step);
        } catch (error) {
          if (!s.allowInputClose || error.code !== "stdin_failed") throw error;
        }
        if (step.expect === undefined) continue;
        const reply = await s.frame();
        if (step.expect_contains)
          assert.ok(reply.raw.includes(step.expect_contains));
        const actual = reply.value;
        if (step.message_prefix) {
          assert.ok(actual.error.message.startsWith(step.message_prefix));
          delete actual.error.message;
        }
        assert.deepEqual(actual, step.expect);
      }
      await s.end();
      const finished = await completed(
        s,
        fixture.effects,
        !fixture.termination,
      );
      if (fixture.termination)
        assert.ok(
          String(finished.transport_error).includes("FrameTooLargeError"),
        );
      results.push({ case: name, status: "passed", levels });
    } finally {
      await s.dispose();
    }
  }
  return results;
}
export async function controlFeasibility(runtime, goChild) {
  const s = await session(runtime, "control-only", goChild);
  try {
    await s.event((e) => e.kind === "ready");
    await s.release("feasibility");
    assert.deepEqual(await s.wait(), { code: 0, signal: null });
    if (s.failure) throw s.failure;
  } finally {
    await s.dispose();
  }
}
export async function replayDuplexSmoke(runtime, goChild) {
  const corpus = parseJSONTokens(
    await readFile(
      new URL(
        "../../../../protocol/v2/fixtures/duplex-correlation.json",
        import.meta.url,
      ),
      "utf8",
    ),
  );
  const results = [];
  for (const name of manifest.duplex_runtime_cases) {
    const recipe = corpus.runtime.find((recipe) => recipe.name === name);
    assert.ok(recipe, "missing named child recipe");
    activeCase = recipe.name;
    const s = await session(runtime, "duplex-smoke", goChild);
    const parents = new Set();
    try {
      for (const step of recipe.steps) {
        switch (step.op) {
          case "send_host":
            parents.add(step.send.id);
            await s.send(JSON.stringify(step.send));
            break;
          case "send_host_response":
            decodeHostRPCDTO("LogResult", JSON.stringify(step.send.result));
            await s.send(JSON.stringify(step.send));
            break;
          case "expect_plugin_request": {
            const frame = await s.frame(
              (value) => typeof value.method === "string",
            );
            subset(frame.value, step.expect);
            assert.equal(frame.value.method, "host/log");
            const params = decodeHostRPCDTO(
              "LogParams",
              inspectEnvelope(frame.raw).fields.get("params"),
            );
            assert.equal(params.context.parent_call.request_owner, "host");
            assert.ok(parents.has(params.context.parent_call.id));
            assert.ok(
              params.context.timeout_ms > 0 &&
                params.context.timeout_ms <= 10000,
            );
            break;
          }
          case "expect_plugin_response": {
            const frame = await s.frame(
              (value) => !Object.hasOwn(value, "method"),
            );
            subset(frame.value, step.expect);
            assert.equal(frame.value.result?.reverse_rpc_version, undefined);
            break;
          }
          case "barrier":
            await s.release("snapshot");
            subset(
              (await s.event((e) => e.kind === "snapshot")).effects,
              step.effects,
            );
            break;
          case "expect_exit": {
            await completed(s, step.effects);
            break;
          }
          default:
            throw new Error("unknown child recipe operation");
        }
      }
      results.push({
        case: recipe.name,
        status: "passed",
        mode: "internal-test-only",
      });
    } finally {
      await s.dispose();
    }
  }
  return results;
}
export async function replayLifecycle(runtime, goChild) {
  const results = [];
  for (const recipe of manifest.lifecycle) {
    assert.equal(recipe.level, "normative");
    activeCase = recipe.name;
    const s = await session(runtime, recipe.profile, goChild);
    try {
      for (const step of recipe.steps) {
        switch (step.op) {
          case "init":
            await s.send(
              JSON.stringify({
                jsonrpc: "2.0",
                id: 1,
                method: "plugin/init",
                params: initParams(),
              }),
            );
            {
              const response = await s.frame();
              assert.equal(response.value.id, 1);
              assert.equal(response.value.result.protocol, 2);
              assert.equal(
                response.value.result.reverse_rpc_version,
                undefined,
              );
            }
            break;
          case "send_host":
            await s.send(JSON.stringify(step.send));
            break;
          case "barrier":
            await s.event((event) => event.kind === step.event);
            break;
          case "signal":
            s.signal(step.signal);
            break;
          case "close_stdin":
            await s.end();
            break;
          case "expect_plugin_response":
            assert.deepEqual((await s.frame()).value, step.expect);
            break;
          case "expect_exit":
            await completed(s, step.effects);
            break;
          default:
            throw new Error("unknown lifecycle operation");
        }
      }
      results.push({
        case: recipe.name,
        status: "passed",
        level: recipe.level,
      });
    } finally {
      await s.dispose();
    }
  }
  return results;
}
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [runtime, goChild] = process.argv.slice(2);
  const abort = () => parentAbort.abort();
  process.once("SIGTERM", abort);
  process.once("SIGINT", abort);
  try {
    await controlFeasibility(runtime, goChild);
    const base = await replayBase(runtime, goChild),
      duplex = await replayDuplexSmoke(runtime, goChild),
      lifecycle = await replayLifecycle(runtime, goChild);
    for (const result of [...base, ...duplex, ...lifecycle])
      console.log(JSON.stringify({ runtime, ...result }));
    console.log(
      `${runtime} child replay PASS (${base.filter((r) => r.status === "passed").length} base, ${duplex.length} internal duplex, ${lifecycle.length} lifecycle; ${base.filter((r) => r.status === "unavailable").length} named proposals)`,
    );
  } catch (error) {
    console.error(
      JSON.stringify({
        runtime,
        status: "failed",
        case: activeCase,
        failure: error.code ?? "assertion_or_fixture_failure",
      }),
    );
    process.exitCode = 1;
  } finally {
    process.removeListener("SIGTERM", abort);
    process.removeListener("SIGINT", abort);
  }
}
