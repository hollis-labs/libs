// Private author-client fixture callbacks; production negotiation is untouched.
import { Writable } from "node:stream";
import { fixturePlugin } from "./fixtures.js";
import { hostClientContext } from "../dist/host-client.js";
import { requestScope } from "../dist/admission.js";
import {
  DeadlineExceededError,
  TransportCancelledError,
} from "../dist/request-control.js";
import { SecretTracker } from "../dist/log.js";
export function childCases(profile, core, event, releases, negotiated = false) {
  const gates = new Map(),
    counts = {},
    base = fixturePlugin("base");
  let init,
    writer,
    armed = false;
  const bump = (name) => (counts[name] = (counts[name] ?? 0) + 1);
  const gate = (name) => {
    if (!gates.has(name)) {
      let resolve;
      const promise = new Promise((r) => (resolve = r));
      gates.set(name, { promise, resolve });
    }
    return gates.get(name);
  };
  releases.set("arm-writer", () => {
    armed = true;
  });
  releases.set("writer", () => gate("writer").resolve());
  const release = (name) => gate(name).resolve();
  const entered = (ctx, name) => {
    const scope = requestScope(ctx),
      id = scope.request.id;
    gate("request-" + id);
    ctx.signal.addEventListener(
      "abort",
      () => {
        const cause = ctx.signal.reason;
        if (
          cause instanceof DeadlineExceededError ||
          cause instanceof TransportCancelledError
        )
          event({
            kind: "aborted",
            id,
            deadline: cause instanceof DeadlineExceededError,
          });
      },
      { once: true },
    );
    bump("entered");
    bump(name);
    event({
      kind: "entered",
      id,
      name,
      deadline: scope.deadline !== undefined,
    });
    return id;
  };
  const output = new Writable({
    write(raw, _enc, done) {
      const send = () => process.stdout.write(raw, done);
      if (armed) {
        armed = false;
        event({ kind: "writer_waiting", bytes: raw.length });
        void gate("writer").promise.then(send);
      } else send();
    },
  });
  const options = { output };
  if (profile === "expanded-queue")
    options.queueLimits = { frames: 32, bytes: 32768 };
  if (profile === "expanded-queue-frames")
    options.queueLimits = { frames: 3, bytes: 8 * 1024 * 1024 };
  if (profile === "expanded-overflow") options.outputFrameBytes = 1024;
  if (profile === "expanded-hung" || profile === "expanded-cleanup-hung")
    options.shutdownTimeoutMs = 200;
  const failure = (err) =>
    !err
      ? { code: "ok", effect_state: "committed" }
      : (err.data ?? {
          code: err.code ?? "fixture_failure",
          effect_state: err.effect_state ?? "unknown",
        });
  const plugin = {
    ...base,
    init(ctx, p) {
      init = p;
      writer = requestScope(ctx).manager.writer;
      if (negotiated) core = requestScope(ctx).manager.core; // Observation only.
      else core.methodTimeoutMS = { ...p.host_services.limits.method_timeout_ms };
      return base.init(ctx, p);
    },
    async load(ctx) {
      const id = entered(ctx, "load");
      await gate("request-" + id).promise;
      event({ kind: "returned", id });
      return {};
    },
    async unload() {
      bump("unload_attempts");
      event({ kind: "unload_started" });
      if (profile === "expanded-cleanup-error")
        throw new Error("fixture cleanup failure");
      if (profile === "expanded-cleanup-panic") throw "fixture cleanup panic";
      if (profile === "expanded-cleanup-hung") await new Promise(() => {});
    },
    health() {
      bump("health");
      return { ok: true };
    },
    effects() {
      return {
        ...counts,
        reverse_pending: core?.pending.size ?? 0,
        ordinary_queued: writer?.ordinary.queue.length ?? 0,
        control_queued: writer?.control.queue.length ?? 0,
        reserved_frames: writer?.control.reservedFrames ?? 0,
        reserved_bytes: writer?.control.reservedBytes ?? 0,
      };
    },
    async command(ctx, p) {
      const id = entered(ctx, p.name),
        wait = gate("request-" + id).promise;
      try {
        if (["hold", "uncooperative"].includes(p.name)) await wait;
        else if (p.name === "cooperative")
          await Promise.race([
            wait,
            new Promise((r) => {
              if (ctx.signal.aborted) r();
              else ctx.signal.addEventListener("abort", r, { once: true });
            }),
          ]);
        else if (p.name === "hung") await new Promise(() => {});
        else if (p.name === "overflow") {
          bump("commits");
          return { action: "message", content: "x".repeat(2048) };
        } else if (p.name === "get" || p.name === "put") {
          const a = JSON.parse(p.args);
          if (
            !Number.isInteger(a.n) ||
            a.n < 1 ||
            a.n > 9 ||
            !Number.isInteger(a.value_bytes ?? 0) ||
            (a.value_bytes ?? 0) < 0 ||
            (a.value_bytes ?? 0) > 65536
          )
            throw new Error("fixture arguments");
          if (a.gate) await wait;
          const h = negotiated ? ctx.host : hostClientContext(
            ctx, core, init, new SecretTracker(),
          ).host;
          if (!h) throw new Error("missing delivered host client");
          const results = await Promise.all(
            Array.from({ length: a.n }, async (_, index) => {
              let err;
              try {
                if (p.name === "get")
                  await h.storageGet({ grant_id: "g-StorageGet", key: a.key });
                else
                  await h.storagePut({
                    grant_id: "g-StoragePut",
                    key: a.key,
                    value: "v".repeat(a.value_bytes ?? 0),
                    expected_revision: null,
                    operation_key: a.operation_key,
                  });
              } catch (e) {
                err = e;
              }
              const result = failure(err);
              event({ kind: "helper_done", id, index, failure: result });
              return result;
            }),
          );
          return { action: "message", content: JSON.stringify(results) };
        }
        return { action: "noop" };
      } finally {
        bump("returned");
        event({ kind: "returned", id });
      }
    },
  };
  return { plugin, options, release };
}
