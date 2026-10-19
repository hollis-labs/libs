import {
  ErrApprovalRequired,
  ErrCancelled,
  HookError,
  fail,
} from "./catalog.js";
import type { Definition, Kind, Validator } from "./catalog.js";
import { data, order, waitFor } from "./registry.js";
import type { Entry, Installed, Invocation, Registry } from "./registry.js";
import { encode, merge, parse, project } from "./json.js";
export type Status =
  | "success"
  | "cancelled"
  | "approval_required"
  | "failed_closed"
  | "completed_with_open_errors"
  | "queued"
  | "caller_cancelled";
export interface HandlerOutcome {
  hook: string;
  owner: string;
  generation: string;
  name: string;
  invocationId: string;
  class: string;
  error?: HookError;
}
export interface DispatchResult {
  invocationId: string;
  status: Status;
  outcomes: HandlerOutcome[];
  value?: string;
  error?: HookError;
  future?: Future;
}
declare const contextBrand: unique symbol;
export interface DispatchContext {
  readonly signal: AbortSignal;
  readonly [contextBrand]: true;
}
export interface DispatchOptions {
  signal?: AbortSignal;
  context?: DispatchContext;
  metadata?: Record<string, string>;
}
export interface ExecutionConfig {
  maxActive?: number;
  maxActivePerOwner?: number;
  maxDepth?: number;
  queueCapacity?: number;
  workers?: number;
}
interface ContextState {
  depth: number;
}
const contexts = new WeakMap<DispatchContext, ContextState>();
function context(signal: AbortSignal, depth: number): DispatchContext {
  const c = Object.freeze({ signal }) as DispatchContext;
  contexts.set(c, { depth });
  return c;
}
interface Dispatch {
  installed: Installed;
  entries: Entry[];
  payload: string;
  metadata: Record<string, string>;
  id: string;
  depth: number;
  controller: AbortController;
  cleanup: () => void;
  permits: Semaphore;
}
interface CallResult {
  value?: string;
  error?: HookError;
}
function hookError(error: unknown): HookError {
  return error instanceof HookError &&
    error.code !== "unavailable" &&
    error.code !== "caller_cancelled"
    ? error
    : new HookError("handler_error");
}
function utf8(json: string): number {
  // Go rejects invalid UTF-8; reject unpaired UTF-16 source surrogates before encoding.
  for (let i = 0; i < json.length; i++) {
    const c = json.charCodeAt(i);
    if (c >= 0xd800 && c <= 0xdbff) {
      const n = json.charCodeAt(++i);
      if (!(n >= 0xdc00 && n <= 0xdfff)) fail("invalid_payload");
    } else if (c >= 0xdc00 && c <= 0xdfff) fail("invalid_payload");
  }
  return new TextEncoder().encode(json).length;
}
function validate(json: string, max: number, validator: Validator): void {
  if (typeof json !== "string" || utf8(json) > max) fail("invalid_payload");
  try {
    parse(json);
    const returned: unknown = validator(json);
    if (returned !== undefined) {
      if (
        returned instanceof Promise ||
        (returned &&
          typeof (returned as { then?: unknown }).then === "function")
      )
        void Promise.resolve(returned).catch(() => {});
      fail("invalid_payload");
    }
  } catch {
    fail("invalid_payload");
  }
}
function combined(
  signals: AbortSignal[],
  timeout?: number,
): { controller: AbortController; cleanup: () => void } {
  const controller = new AbortController();
  const abort = () => controller.abort();
  for (const s of signals) {
    if (s.aborted) controller.abort();
    else s.addEventListener("abort", abort, { once: true });
  }
  // Fractional positive limits are accepted; timers have platform granularity.
  let timer: ReturnType<typeof setTimeout> | undefined;
  if (timeout !== undefined) {
    const deadline = performance.now() + timeout;
    const tick = () => {
      const left = deadline - performance.now();
      if (left <= 0) abort();
      else timer = setTimeout(tick, Math.min(left, 2147483647));
    };
    timer = setTimeout(tick, Math.min(timeout, 2147483647));
  }
  return {
    controller,
    cleanup: () => {
      if (timer !== undefined) clearTimeout(timer);
      for (const s of signals) s.removeEventListener("abort", abort);
    },
  };
}
class Semaphore {
  private count = 0;
  private waiting: (() => void)[] = [];
  constructor(private readonly limit: number) {}
  async acquire(signal: AbortSignal): Promise<() => void> {
    while (this.count >= this.limit) {
      let wake!: () => void;
      const ready = new Promise<void>((resolve) => {
        wake = resolve;
        this.waiting.push(wake);
      });
      try {
        await waitFor(ready, signal);
      } finally {
        this.waiting = this.waiting.filter((w) => w !== wake);
      }
    }
    if (signal.aborted) throw new HookError("timeout");
    this.count++;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.count--;
      const wakes = this.waiting.splice(0);
      wakes.forEach((w) => w());
    };
  }
}
export class Future {
  constructor(private readonly result: Promise<DispatchResult>) {}
  async await(signal?: AbortSignal): Promise<DispatchResult> {
    const result = signal
      ? await waitFor(this.result, signal)
      : await this.result;
    return {
      ...result,
      ...(result.error
        ? { error: new HookError(result.error.code, result.error.message) }
        : {}),
      outcomes: result.outcomes.map((o) => ({
        ...o,
        ...(o.error
          ? { error: new HookError(o.error.code, o.error.message) }
          : {}),
      })),
    };
  }
}
export class Pending {
  private used = false;
  constructor(
    private readonly commitFn: () => DispatchResult,
    private readonly cleanup: () => void,
  ) {}
  commit(): DispatchResult {
    if (this.used) fail("unavailable");
    this.used = true;
    return this.commitFn();
  }
  rollback(): void {
    if (!this.used) {
      this.used = true;
      this.cleanup();
    }
  }
}
export class Engine {
  private readonly config: Required<ExecutionConfig>;
  private readonly lifetime = new AbortController();
  private readonly total: Semaphore;
  private readonly owners = new Map<string, Semaphore>();
  private queue: {
    dispatch: Dispatch;
    resolve: (r: DispatchResult) => void;
  }[] = [];
  private running = 0;
  private draining = false;
  private workers = new Set<Promise<unknown>>();
  private sequence = 0;
  constructor(
    private readonly registry: Registry,
    config: ExecutionConfig = {},
  ) {
    const defaults: Required<ExecutionConfig> = {
      maxActive: 64,
      maxActivePerOwner: 8,
      maxDepth: 8,
      queueCapacity: 256,
      workers: 4,
    };
    for (const key of Object.keys(config))
      if (!Object.hasOwn(defaults, key)) fail("invalid_options");
    for (const key of Object.keys(defaults) as (keyof ExecutionConfig)[]) {
      const v = config[key];
      if (v !== undefined && (!Number.isSafeInteger(v) || v < 0))
        fail("invalid_options");
      if (v) defaults[key] = v;
    }
    const d = data(registry);
    if (d.attached) fail("duplicate");
    d.attached = true;
    this.config = defaults;
    this.total = new Semaphore(defaults.maxActive);
  }
  private prepare(
    hook: string,
    payload: string,
    kind: Kind,
    options: DispatchOptions,
    detached: boolean,
  ): Dispatch {
    if (
      !options ||
      (options.context !== undefined && !contexts.has(options.context)) ||
      (options.signal !== undefined && !(options.signal instanceof AbortSignal))
    )
      fail("invalid_options");
    const installed = data(this.registry).definitions.get(hook);
    if (!installed) fail("unknown_hook", hook);
    if (installed.definition.kind !== kind) fail("invalid_options");
    const parent =
      options.context === undefined ? undefined : contexts.get(options.context);
    if (options.context && !parent) fail("invalid_options");
    const depth = parent?.depth ?? 0;
    if (depth >= this.config.maxDepth) fail("depth_rejected");
    if (this.lifetime.signal.aborted) fail("engine_closed");
    const signals = [this.lifetime.signal];
    if (!detached) {
      if (options.signal) signals.push(options.signal);
      if (options.context) signals.push(options.context.signal);
    }
    const linked = combined(signals, installed.definition.budget_ms);
    try {
      validate(
        payload,
        installed.definition.max_payload_bytes,
        installed.validators.input,
      );
      if (linked.controller.signal.aborted) fail("caller_cancelled");
      const metadata: Record<string, string> = Object.create(null) as Record<
        string,
        string
      >;
      for (const [key, value] of Object.entries(options.metadata ?? {})) {
        if (typeof value !== "string") fail("invalid_options");
        metadata[key] = value;
      }
      return {
        installed,
        payload,
        metadata,
        depth: depth + 1,
        entries: [...data(this.registry).entries.values()]
          .filter(
            (e) => !e.removed && !e.claimed && e.registration.hook === hook,
          )
          .sort(order),
        id: String(++this.sequence),
        ...linked,
        permits: new Semaphore(installed.definition.max_parallelism),
      };
    } catch (e) {
      linked.cleanup();
      throw e;
    }
  }
  async doAction(
    hook: string,
    payload: string,
    options: DispatchOptions = {},
  ): Promise<DispatchResult> {
    const mode = data(this.registry).definitions.get(hook)?.definition.mode;
    if (mode === "after_commit") fail("commit_required");
    const d = this.prepare(hook, payload, "action", options, mode === "async");
    if (mode === "async") return this.enqueue(d);
    try {
      return await this.execute(d);
    } finally {
      d.cleanup();
    }
  }
  async applyFilters(
    hook: string,
    payload: string,
    options: DispatchOptions = {},
  ): Promise<DispatchResult> {
    const d = this.prepare(hook, payload, "filter", options, false);
    try {
      return await this.execute(d);
    } finally {
      d.cleanup();
    }
  }
  /** Explicit asynchronous caller variant; catalog mode remains authoritative. */
  doActionAsync(
    hook: string,
    payload: string,
    options: DispatchOptions = {},
  ): Promise<DispatchResult> {
    return this.doAction(hook, payload, options);
  }
  applyFiltersAsync(
    hook: string,
    payload: string,
    options: DispatchOptions = {},
  ): Promise<DispatchResult> {
    return this.applyFilters(hook, payload, options);
  }
  prepareAfterCommit(
    hook: string,
    payload: string,
    options: DispatchOptions = {},
  ): Pending {
    const d = this.prepare(hook, payload, "action", options, true);
    if (d.installed.definition.mode !== "after_commit") {
      d.cleanup();
      fail("invalid_options");
    }
    return new Pending(() => this.enqueue(d), d.cleanup);
  }
  private enqueue(d: Dispatch): DispatchResult {
    if (
      this.lifetime.signal.aborted ||
      this.queue.length >= this.config.queueCapacity
    ) {
      d.cleanup();
      fail(this.lifetime.signal.aborted ? "engine_closed" : "queue_full");
    }
    const future = new Future(
      new Promise<DispatchResult>((resolve) =>
        this.queue.push({ dispatch: d, resolve }),
      ),
    );
    // Defer draining so admission is atomic and FIFO within the current turn.
    if (!this.draining) {
      this.draining = true;
      queueMicrotask(() => {
        this.draining = false;
        this.drain();
      });
    }
    return { invocationId: d.id, status: "queued", outcomes: [], future };
  }
  private drain(): void {
    while (this.running < this.config.workers && this.queue.length) {
      const next = this.queue.shift()!;
      this.running++;
      const work = this.execute(next.dispatch)
        .then(next.resolve)
        .finally(() => {
          next.dispatch.cleanup();
          this.running--;
          this.workers.delete(work);
          this.drain();
        });
      this.workers.add(work);
    }
  }
  async shutdown(signal?: AbortSignal): Promise<void> {
    this.lifetime.abort();
    this.drain();
    const drained = async () => {
      while (this.workers.size || this.queue.length) {
        this.drain();
        await Promise.allSettled([...this.workers]);
      }
    };
    if (signal) await waitFor(drained(), signal);
    else await drained();
  }
  private async invoke(
    d: Dispatch,
    entry: Entry,
    full: string,
  ): Promise<CallResult> {
    const def = d.installed.definition;
    const o = entry.registration.options;
    const linked = combined(
      [d.controller.signal, entry.cancellation.signal],
      o.timeoutMs,
    );
    const signal = linked.controller.signal;
    const releases: (() => void)[] = [];
    let started = false;
    try {
      let owner = this.owners.get(entry.state.key);
      if (!owner) {
        owner = new Semaphore(this.config.maxActivePerOwner);
        this.owners.set(entry.state.key, owner);
      }
      releases.push(await d.permits.acquire(signal));
      releases.push(await owner.acquire(signal));
      releases.push(await this.total.acquire(signal));
      if (entry.removed || entry.claimed || entry.state.disposed)
        return { error: new HookError("unavailable") };
      if (signal.aborted) return { error: new HookError("unavailable") };
      if (o.once) entry.claimed = true;
      const view = o.view ? def.views[o.view]! : undefined;
      const input =
        view === undefined
          ? def.mode === "parallel"
            ? full
            : encode(parse(full))
          : encode(project(parse(full), view));
      const r = entry.registration;
      const invocation: Invocation = {
        id: d.id,
        hook: r.hook,
        owner: r.owner,
        generation: r.generation,
        registration: r.name,
        payload: input,
        metadata: { ...d.metadata },
        context: context(signal, d.depth),
      };
      const returned = Promise.resolve().then(() => {
        try {
          return entry.handler(invocation);
        } catch (e) {
          throw e instanceof HookError ? e : new HookError("panic");
        }
      });
      const actual = returned
        .then<CallResult, CallResult>(
          (value) => {
            if (entry.removed || entry.state.disposed)
              return { error: new HookError("unavailable") };
            if (def.kind === "action") return {};
            try {
              if (
                typeof value !== "string" ||
                utf8(value) > def.max_payload_bytes
              )
                return { error: new HookError("invalid_output") };
              const merged = merge(full, input, value, def.mutable_paths, view);
              validate(
                merged,
                def.max_payload_bytes,
                d.installed.validators.output!,
              );
              return { value: merged };
            } catch {
              return { error: new HookError("invalid_output") };
            }
          },
          (e) => ({ error: hookError(e) }),
        )
        .finally(() => {
          entry.active.delete(actual);
          entry.state.active.delete(actual);
          if ((entry.claimed || entry.removed) && !entry.active.size)
            data(this.registry).entries.delete(entry.registration.handle);
          releases.reverse().forEach((release) => release());
          linked.cleanup();
        });
      entry.active.add(actual);
      entry.state.active.add(actual);
      started = true;
      try {
        return await waitFor(actual, signal);
      } catch {
        return {
          error: new HookError(
            entry.removed || entry.state.disposed
              ? "unavailable"
              : d.controller.signal.aborted
                ? "caller_cancelled"
                : "timeout",
          ),
        };
      }
    } catch {
      return { error: new HookError("unavailable") };
    } finally {
      if (!started) {
        releases.reverse().forEach((release) => release());
        linked.cleanup();
      }
    }
  }
  private outcome(d: Dispatch, e: Entry, result: CallResult): HandlerOutcome {
    const r = e.registration;
    return {
      hook: r.hook,
      owner: r.owner,
      generation: r.generation,
      name: r.name,
      invocationId: d.id,
      class: result.error?.code ?? "success",
      ...(result.error ? { error: result.error } : {}),
    };
  }
  private async execute(d: Dispatch): Promise<DispatchResult> {
    const def: Definition = d.installed.definition;
    const result: DispatchResult = {
      invocationId: d.id,
      status: "success",
      outcomes: [],
    };
    let current = d.payload;
    const accept = (e: Entry, call: CallResult): boolean => {
      result.outcomes.push(this.outcome(d, e, call));
      if (d.controller.signal.aborted) {
        result.status = "caller_cancelled";
        result.error = new HookError("caller_cancelled");
        return false;
      }
      if (!call.error) {
        if (def.kind === "filter") current = call.value!;
        return true;
      }
      if (
        def.mode === "bail" &&
        (call.error === ErrCancelled || call.error.code === "cancelled")
      ) {
        result.status = "cancelled";
        result.error = ErrCancelled;
        return false;
      }
      if (
        def.mode === "bail" &&
        (call.error === ErrApprovalRequired ||
          call.error.code === "approval_required")
      ) {
        result.status = "approval_required";
        result.error = ErrApprovalRequired;
        return false;
      }
      if (e.registration.options.onError === "closed") {
        result.status = "failed_closed";
        result.error ??= call.error;
        return false;
      }
      if (result.status !== "failed_closed")
        result.status = "completed_with_open_errors";
      return true;
    };
    if (def.mode === "parallel") {
      // Start/admit in registration order; observe outcomes in that same order.
      const batch: { entry: Entry; promise: Promise<CallResult> }[] = [];
      const collect = async (pending: {
        entry: Entry;
        promise: Promise<CallResult>;
      }) => {
        accept(pending.entry, await pending.promise);
      };
      for (const entry of d.entries) {
        if (d.controller.signal.aborted) break;
        if (batch.length >= def.max_parallelism) await collect(batch.shift()!);
        if (d.controller.signal.aborted) break;
        batch.push({ entry, promise: this.invoke(d, entry, current) });
      }
      for (const pending of batch) await collect(pending);
    } else {
      for (const e of d.entries) {
        if (d.controller.signal.aborted) break;
        const call = await this.invoke(d, e, current);
        if (!accept(e, call)) break;
      }
    }
    if (d.controller.signal.aborted) {
      result.status = "caller_cancelled";
      result.error = new HookError("caller_cancelled");
    }
    if (
      def.kind === "filter" &&
      (result.status === "success" ||
        result.status === "completed_with_open_errors")
    )
      result.value = current;
    return result;
  }
}
