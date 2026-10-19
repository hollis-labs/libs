import { describe, expect, it, vi } from "vitest";
import { Engine, HookError } from "../src/index.js";
import { registry } from "./fixtures.js";
function deferred<T = void>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}
async function turns() {
  for (let i = 0; i < 30; i++) await Promise.resolve();
}
describe("bounded lifecycle", () => {
  it("timeout retains permits until actual completion and does not consume waiting once", async () => {
    vi.useFakeTimers();
    const r = registry();
    const e = new Engine(r, { maxActive: 1, maxActivePerOwner: 1 });
    const a = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    const b = r.newScope({
      owner: "b",
      generation: "1",
      hooks: ["event.observe"],
    });
    const gate = deferred();
    const began = deferred();
    a.addAction("event.observe", "stuck", { timeoutMs: 10 }, async () => {
      began.resolve();
      await gate.promise;
    });
    let onceCalls = 0;
    b.addAction("event.observe", "once", { once: true, timeoutMs: 20 }, () => {
      onceCalls++;
    });
    try {
      const first = e.doAction("event.observe", "{}");
      await began.promise;
      await vi.advanceTimersByTimeAsync(30);
      const result = await first;
      expect(result.outcomes.map((o) => o.class)).toEqual([
        "timeout",
        "unavailable",
      ]);
      expect(onceCalls).toBe(0);
      expect(b.registrations()).toHaveLength(1);
      a.remove(a.registrations()[0]!.handle);
      gate.resolve();
      await turns();
      const next = await e.doAction("event.observe", "{}");
      expect(next.status).toBe("success");
      expect(onceCalls).toBe(1);
    } finally {
      gate.resolve();
      await e.shutdown();
      vi.useRealTimers();
    }
  });
  it("concurrent dispatch once claims only one attempt", async () => {
    const r = registry();
    const e = new Engine(r);
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    const gate = deferred();
    let calls = 0;
    s.addAction("event.observe", "once", { once: true }, async () => {
      calls++;
      await gate.promise;
    });
    const results = Array.from({ length: 20 }, () =>
      e.doAction("event.observe", "{}"),
    );
    await turns();
    expect(calls).toBe(1);
    gate.resolve();
    await Promise.all(results);
    expect(calls).toBe(1);
    await e.shutdown();
  });
  it("removal wakes a capacity waiter, invalidates late output and preserves newer generations", async () => {
    const r = registry();
    const e = new Engine(r, { maxActive: 1 });
    const a = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["value.change"],
    });
    const b = r.newScope({
      owner: "b",
      generation: "1",
      hooks: ["value.change"],
    });
    const gate = deferred<string>();
    const began = deferred();
    const h = a.addFilter("value.change", "stuck", {}, () => {
      began.resolve();
      return gate.promise;
    });
    let bCalls = 0;
    const bh = b.addFilter("value.change", "waiting", {}, () => {
      bCalls++;
      return '{"value":3}';
    });
    const call = e.applyFilters("value.change", '{"value":1}');
    await began.promise;
    expect(b.removeAction(bh)).toBe(false);
    expect(b.remove(bh)).toBe(true);
    expect(b.remove(h)).toBe(false);
    const signal = AbortSignal.timeout(10);
    await expect(a.dispose(signal)).rejects.toThrow();
    expect(() =>
      a.addFilter("value.change", "new", {}, () => "{}"),
    ).toThrowError(HookError);
    expect(() =>
      r.newScope({ owner: "a", generation: "1", hooks: [] }),
    ).toThrowError(HookError);
    const newer = r.newScope({
      owner: "a",
      generation: "2",
      hooks: ["value.change"],
    });
    newer.addFilter("value.change", "new", {}, () => '{"value":4}');
    gate.resolve('{"value":2}');
    const result = await call;
    expect(result.value).toBe('{"value":1}');
    expect(bCalls).toBe(0);
    expect((await e.applyFilters("value.change", '{"value":1}')).value).toBe(
      '{"value":4}',
    );
    await e.shutdown();
  });
  it("snapshot excludes additions but rechecks removals before starts", async () => {
    const r = registry();
    const e = new Engine(r);
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    let calls: string[] = [];
    let changed = false;
    s.addAction("event.observe", "first", {}, () => {
      calls.push("first");
      if (!changed) {
        changed = true;
        s.remove(later);
        s.addAction("event.observe", "new", {}, () => {
          calls.push("new");
        });
      }
    });
    const later = s.addAction("event.observe", "later", {}, () => {
      calls.push("later");
    });
    const one = await e.doAction("event.observe", "{}");
    expect(calls).toEqual(["first"]);
    expect(one.outcomes.map((o) => o.class)).toEqual([
      "success",
      "unavailable",
    ]);
    calls = [];
    await e.doAction("event.observe", "{}");
    expect(calls).toEqual(["first", "new"]);
    await e.shutdown();
  });
  it("nested depth uses supplied context including detached async", async () => {
    const r = registry();
    const e = new Engine(r, { maxDepth: 1 });
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe", "event.async"],
    });
    s.addAction("event.observe", "nested", {}, async (i) => {
      await e.doAction("event.async", "{}", { context: i.context });
    });
    const result = await e.doAction("event.observe", "{}");
    expect(result.outcomes[0]!.class).toBe("depth_rejected");
    await e.shutdown();
  });
  it("bounded FIFO async queue rejects overload, detaches cancellation and copies futures", async () => {
    const r = registry();
    const e = new Engine(r, { workers: 1, queueCapacity: 1 });
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.async"],
    });
    const gate = deferred();
    const began = deferred();
    const seen: string[] = [];
    s.addAction("event.async", "handler", {}, async (i) => {
      seen.push(i.payload);
      began.resolve();
      await gate.promise;
    });
    const controller = new AbortController();
    const receipt = await e.doAction("event.async", '{"first":true}', {
      signal: controller.signal,
    });
    controller.abort();
    await began.promise;
    const second = await e.doAction("event.async", '{"second":true}');
    await expect(
      e.doAction("event.async", '{"third":true}'),
    ).rejects.toMatchObject({ code: "queue_full" });
    gate.resolve();
    const first = await receipt.future!.await();
    expect(first.status).toBe("success");
    first.outcomes.splice(0);
    expect((await receipt.future!.await()).outcomes).toHaveLength(1);
    await second.future!.await();
    expect(seen).toEqual(['{"first":true}', '{"second":true}']);
    await e.shutdown();
  });
  it("whole dispatch cancellation overrides open and after-commit capabilities are one-shot", async () => {
    const r = registry();
    const e = new Engine(r);
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe", "event.committed"],
    });
    const gate = deferred();
    const began = deferred();
    s.addAction("event.observe", "handler", {}, async () => {
      began.resolve();
      await gate.promise;
    });
    const abort = new AbortController();
    const work = e.doAction("event.observe", "{}", { signal: abort.signal });
    await began.promise;
    abort.abort();
    expect((await work).status).toBe("caller_cancelled");
    gate.resolve();
    let calls = 0;
    s.addAction("event.committed", "commit", {}, () => {
      calls++;
    });
    const pending = e.prepareAfterCommit("event.committed", "{}");
    pending.rollback();
    expect(() => pending.commit()).toThrowError(HookError);
    expect(calls).toBe(0);
    const committed = e.prepareAfterCommit("event.committed", "{}");
    const receipt = committed.commit();
    expect(() => committed.commit()).toThrowError(HookError);
    await receipt.future!.await();
    expect(calls).toBe(1);
    await e.shutdown();
  });
  it("handler metadata and catalog snapshots are private", async () => {
    const r = registry();
    const e = new Engine(r);
    const s = r.newScope({
      owner: "a",
      generation: "1",
      hooks: ["event.observe"],
    });
    const metadata = { key: "original" };
    s.addAction("event.observe", "first", {}, (i) => {
      i.metadata.key = "changed";
      i.payload = "{}";
    });
    s.addAction("event.observe", "second", {}, (i) => {
      expect(i.metadata).toEqual(metadata);
      expect(i.payload).toBe('{"value":1}');
    });
    await e.doAction("event.observe", '{"value":1}', { metadata });
    expect(metadata.key).toBe("original");
    await e.shutdown();
  });
});
