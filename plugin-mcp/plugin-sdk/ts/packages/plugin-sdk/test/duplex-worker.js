import { fixtureControl, fixtureEvent } from "./child-control.js";
const profile = process.argv[2];
if (profile?.startsWith("fault:")) {
  const fault = profile.slice(6);
  process.stdin.on("error", () => {});
  process.stdout.on("error", () => {});
  process.stderr.on("error", () => {});
  if (fault === "early") process.exit(23);
  else if (fault === "oversized") process.stdout.write("x".repeat(1024) + "\n");
  else if (fault === "no-lf") {
    process.stdout.write("{}");
    process.exitCode = 0;
  } else if (fault === "stderr") {
    let count = 0;
    const flood = () => {
      while (count++ < 12000) {
        if (!process.stderr.write("x".repeat(99) + "\n")) {
          process.stderr.once("drain", flood);
          return;
        }
      }
    };
    flood();
  }
  if (fault !== "no-lf") {
    if (fault === "hung") process.on("SIGTERM", () => {});
    fixtureEvent({ kind: "ready" });
    setInterval(() => {}, 1000);
  }
} else {
  const releases = new Map();
  const stop = fixtureControl((value) => {
    fixtureEvent({ kind: "control_received", seq: value.seq });
    releases.get(value.gate)?.();
  });
  fixtureEvent({ kind: "ready" });
  if (profile === "control-only") {
    releases.set("feasibility", () => stop());
  } else {
    const { fixturePlugin } = await import("./fixtures.js");
    const { serveConnection } = await import("../dist/serve.js");
    const { Correlation } = await import("../dist/correlation.js");
    const core = new Correlation(profile === "duplex-smoke");
    core.methodTimeoutMS = { "host/log": 10000 }; // Fixture-only offer, never a SDK fallback.
    let plugin = fixturePlugin(profile === "duplex-smoke" ? "base" : profile),
      unloads = 0;
    if (profile === "full") {
      const command = plugin.command;
      plugin.command = async (ctx, params) => {
        if (params.name !== "wait-for-abort") return command(ctx, params);
        fixtureEvent({ kind: "handler_waiting" });
        await new Promise((resolve) => {
          if (ctx.signal.aborted) resolve();
          else ctx.signal.addEventListener("abort", resolve, { once: true });
        });
        return { action: "noop" };
      };
    }
    const unload = plugin.unload;
    plugin.unload = async (ctx) => {
      unloads++;
      fixtureEvent({ kind: "unload_started", count: unloads });
      return unload(ctx);
    };
    if (profile === "duplex-smoke") {
      let healths = 0,
        initialized = false;
      const log = async (ctx) => {
        const { requestScope } = await import("../dist/admission.js");
        const scope = requestScope(ctx);
        return core.call(
          "host/log",
          JSON.stringify({
            grant_id: "g",
            context: {
              binding_id: "b",
              timeout_ms: 10000,
              parent_call: { request_owner: "host", id: scope.request.id },
            },
            level: "info",
            message: "ready",
          }),
          ctx,
        );
      };
      const init = plugin.init;
      plugin.init = async (ctx, p) => {
        await log(ctx);
        initialized = true;
        return init(ctx, p);
      };
      plugin.unload = async (ctx) => {
        unloads++;
        fixtureEvent({ kind: "unload_started", count: unloads });
        await log(ctx);
      };
      plugin.health = () => {
        healths++;
        return { ok: true };
      };
      plugin.effects = () => ({
        initialized,
        unload_attempts: unloads,
        health_calls: healths,
      });
    }
    releases.set("snapshot", () =>
      fixtureEvent({
        kind: "snapshot",
        effects: plugin.effects?.() ?? { unload_attempts: unloads },
      }),
    );
    try {
      await serveConnection(plugin, {}, core);
      fixtureEvent({
        kind: "finished",
        effects: plugin.effects?.() ?? { unload_attempts: unloads },
        transport_error: null,
      });
    } catch (error) {
      fixtureEvent({
        kind: "finished",
        effects: { unload_attempts: unloads },
        transport_error: error.name,
      });
      process.exitCode = 1;
    } finally {
      stop();
    }
  }
}
