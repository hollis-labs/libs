import { fail, readCatalog, HookError } from "./catalog.js";
import type {
  CompileValidators,
  Definition,
  ErrorPolicy,
  Kind,
  Validators,
  Deprecation,
} from "./catalog.js";
import { parse, encode } from "./json.js";
import type { Json } from "./json.js";
import type { DispatchContext } from "./engine.js";
declare const handleBrand: unique symbol;
export interface Handle {
  readonly [handleBrand]: true;
}
export interface Options {
  priority?: number;
  once?: boolean;
  timeoutMs?: number;
  onError?: ErrorPolicy;
  view?: string;
  schemaDigest?: string;
}
export interface ResolvedOptions {
  priority: number;
  once: boolean;
  timeoutMs: number;
  onError: ErrorPolicy;
  view: string;
}
export interface Registration {
  handle: Handle;
  owner: string;
  generation: string;
  hook: string;
  name: string;
  sequence: number;
  options: ResolvedOptions;
  warnings: Deprecation[];
}
export interface ScopeConfig {
  owner: string;
  generation: string;
  hooks: readonly string[];
  remote?: boolean;
}
export interface Invocation {
  id: string;
  hook: string;
  owner: string;
  generation: string;
  registration: string;
  payload: string;
  metadata: Record<string, string>;
  context: DispatchContext;
}
export type ActionHandler = (invocation: Invocation) => void | Promise<void>;
export type FilterHandler = (
  invocation: Invocation,
) => string | Promise<string>;
export interface Installed {
  definition: Definition;
  validators: Validators;
}
export interface Entry {
  registration: Registration;
  state: State;
  handler: ActionHandler | FilterHandler;
  removed: boolean;
  claimed: boolean;
  cancellation: AbortController;
  active: Set<Promise<unknown>>;
}
export interface State {
  owner: string;
  generation: string;
  key: string;
  allowed: Set<string>;
  disposed: boolean;
  remote: boolean;
  active: Set<Promise<unknown>>;
}
// Internal storage is module-private; plugin scope objects expose no registry reference.
const registryData = new WeakMap<Registry, Data>();
const scopes = new WeakMap<Scope, { registry: Registry; state: State }>();
interface Data {
  definitions: Map<string, Installed>;
  states: Map<string, State>;
  entries: Map<Handle, Entry>;
  sequence: number;
  attached: boolean;
  catalogJSON: string;
}
export const data = (registry: Registry): Data =>
  registryData.get(registry) ?? fail("invalid_options");
export const scopeData = (scope: Scope) =>
  scopes.get(scope) ?? fail("invalid_options");
export class Registry {
  constructor(catalogJSON: string, compile: CompileValidators) {
    const c = readCatalog(catalogJSON);
    const definitions = new Map<string, Installed>();
    if (typeof compile !== "function") fail("invalid_definition");
    const rawDocument = parse(catalogJSON) as {
      catalog_version: Json;
      definitions: { [key: string]: Json }[];
    };
    let index = 0;
    for (const d of c.definitions) {
      const raw = rawDocument.definitions[index++]!;
      const validators = compile(structuredClone(d), {
        input: encode(raw.input_schema!),
        ...(raw.output_schema === undefined
          ? {}
          : { output: encode(raw.output_schema) }),
      });
      if (
        !validators ||
        typeof validators.input !== "function" ||
        (d.kind === "filter"
          ? typeof validators.output !== "function"
          : validators.output !== undefined)
      )
        fail("invalid_definition");
      definitions.set(d.name, { definition: d, validators: { ...validators } });
    }
    rawDocument.definitions.sort((a, b) =>
      String(a.name) < String(b.name)
        ? -1
        : String(a.name) > String(b.name)
          ? 1
          : 0,
    );
    registryData.set(this, {
      definitions,
      states: new Map(),
      entries: new Map(),
      sequence: 0,
      attached: false,
      catalogJSON: encode(rawDocument),
    });
  }
  catalog(): string {
    return data(this).catalogJSON;
  }
  newScope(config: ScopeConfig): Scope {
    const d = data(this);
    if (
      !config ||
      typeof config.owner !== "string" ||
      !config.owner ||
      typeof config.generation !== "string" ||
      !config.generation ||
      !Array.isArray(config.hooks) ||
      (config.remote !== undefined && typeof config.remote !== "boolean")
    )
      fail("unauthorized");
    const allowed = new Set<string>();
    for (const n of config.hooks) {
      const installed = d.definitions.get(n);
      if (!installed) fail("unknown_hook", String(n));
      if (config.remote && !installed.definition.remote_ok)
        fail("unauthorized");
      allowed.add(n);
    }
    const key = JSON.stringify([config.owner, config.generation]);
    if (d.states.has(key)) fail("duplicate");
    const state: State = {
      owner: config.owner,
      generation: config.generation,
      key,
      allowed,
      disposed: false,
      remote: config.remote ?? false,
      active: new Set(),
    };
    d.states.set(key, state);
    return new Scope(this, state);
  }
  remove(handle: Handle): boolean {
    const entry = data(this).entries.get(handle);
    if (!entry || entry.removed) return false;
    entry.removed = true;
    entry.cancellation.abort();
    if (!entry.active.size) data(this).entries.delete(handle);
    return true;
  }
  removeByPlugin(owner: string, generation: string): number {
    let n = 0;
    for (const [h, e] of data(this).entries)
      if (
        e.state.owner === owner &&
        e.state.generation === generation &&
        this.remove(h)
      )
        n++;
    return n;
  }
}
export class Scope {
  /** Host-created through Registry.newScope; constructing a scope yourself grants nothing. */
  constructor(registry: Registry, state: State) {
    if (data(registry).states.get(state.key) !== state) fail("unauthorized");
    scopes.set(this, { registry, state });
  }
  validateRegistration(
    hook: string,
    name: string,
    kind: Kind,
    options: Options = {},
  ): { options: ResolvedOptions; warnings: Deprecation[] } {
    const { registry, state } = scopeData(this);
    const d = data(registry).definitions.get(hook)?.definition;
    if (state.disposed) fail("disposed");
    if (!d) fail("unknown_hook", hook);
    if (!state.allowed.has(hook)) fail("unauthorized");
    if (typeof name !== "string" || !name || d.kind !== kind)
      fail("invalid_options");
    if (
      !options ||
      Object.keys(options).some(
        (k) =>
          ![
            "priority",
            "once",
            "timeoutMs",
            "onError",
            "view",
            "schemaDigest",
          ].includes(k),
      )
    )
      fail("invalid_options");
    if (
      (options.once !== undefined && typeof options.once !== "boolean") ||
      (options.view !== undefined && typeof options.view !== "string")
    )
      fail("invalid_options");
    const o: ResolvedOptions = {
      priority: options.priority === undefined ? 10 : options.priority,
      once: options.once ?? false,
      timeoutMs:
        options.timeoutMs === undefined
          ? d.handler_timeout_ms
          : options.timeoutMs,
      onError:
        options.onError === undefined ? d.on_error_default : options.onError,
      view: options.view ?? "",
    };
    if (
      !Number.isSafeInteger(o.priority) ||
      typeof o.once !== "boolean" ||
      typeof o.timeoutMs !== "number" ||
      !Number.isFinite(o.timeoutMs) ||
      o.timeoutMs <= 0 ||
      o.timeoutMs > d.handler_timeout_ms ||
      !d.allowed_on_error.includes(o.onError) ||
      typeof o.view !== "string" ||
      (o.view && !Object.hasOwn(d.views, o.view)) ||
      (options.schemaDigest !== undefined &&
        options.schemaDigest !== d.schema_digest)
    )
      fail("invalid_options");
    if (d.required_view && o.view !== d.required_view) fail("unauthorized");
    return {
      options: o,
      warnings: d.deprecated ? [structuredClone(d.deprecated)] : [],
    };
  }
  addAction(
    hook: string,
    name: string,
    options: Options,
    handler: ActionHandler,
  ): Handle {
    return this.add(hook, name, options, handler, "action");
  }
  addFilter(
    hook: string,
    name: string,
    options: Options,
    handler: FilterHandler,
  ): Handle {
    return this.add(hook, name, options, handler, "filter");
  }
  private add(
    hook: string,
    name: string,
    options: Options,
    handler: ActionHandler | FilterHandler,
    kind: Kind,
  ): Handle {
    const policy = this.validateRegistration(hook, name, kind, options);
    if (typeof handler !== "function") fail("invalid_options");
    const { registry, state } = scopeData(this);
    if (state.remote) fail("unauthorized");
    const d = data(registry);
    const entries = [...d.entries.values()];
    if (
      entries.some(
        (e) => e.state === state && !e.removed && e.registration.name === name,
      )
    )
      fail("duplicate");
    if (
      entries.filter(
        (e) => !e.removed && !e.claimed && e.registration.hook === hook,
      ).length >= d.definitions.get(hook)!.definition.max_handlers
    )
      fail("unavailable");
    const handle = Object.freeze(Object.create(null)) as Handle;
    const registration: Registration = {
      handle,
      owner: state.owner,
      generation: state.generation,
      hook,
      name,
      sequence: ++d.sequence,
      ...policy,
    };
    d.entries.set(handle, {
      registration,
      state,
      handler,
      removed: false,
      claimed: false,
      cancellation: new AbortController(),
      active: new Set(),
    });
    return handle;
  }
  remove(handle: Handle): boolean {
    const { registry, state } = scopeData(this);
    return (
      data(registry).entries.get(handle)?.state === state &&
      registry.remove(handle)
    );
  }
  removeAction(handle: Handle): boolean {
    return this.removeKind(handle, "action");
  }
  removeFilter(handle: Handle): boolean {
    return this.removeKind(handle, "filter");
  }
  private removeKind(handle: Handle, kind: Kind): boolean {
    const { registry } = scopeData(this);
    const e = data(registry).entries.get(handle);
    return (
      !!e &&
      data(registry).definitions.get(e.registration.hook)!.definition.kind ===
        kind &&
      this.remove(handle)
    );
  }
  registrations(): Registration[] {
    const { registry, state } = scopeData(this);
    return [...data(registry).entries.values()]
      .filter((e) => e.state === state && !e.removed && !e.claimed)
      .sort(order)
      .map((e) => ({
        ...e.registration,
        options: { ...e.registration.options },
        warnings: structuredClone(e.registration.warnings),
      }));
  }
  async dispose(signal?: AbortSignal): Promise<void> {
    const { registry, state } = scopeData(this);
    state.disposed = true;
    registry.removeByPlugin(state.owner, state.generation);
    if (!state.active.size) return;
    const waiting = Promise.allSettled([...state.active]);
    if (!signal) {
      await waiting;
      return;
    }
    await waitFor(waiting, signal);
  }
}
export const order = (a: Entry, b: Entry): number =>
  a.registration.options.priority - b.registration.options.priority ||
  a.registration.sequence - b.registration.sequence;
export function waitFor<T>(
  promise: Promise<T>,
  signal: AbortSignal,
): Promise<T> {
  if (signal.aborted) return Promise.reject(new HookError("caller_cancelled"));
  return new Promise<T>((resolve, reject) => {
    const cancel = () => {
      signal.removeEventListener("abort", cancel);
      reject(new HookError("caller_cancelled"));
    };
    signal.addEventListener("abort", cancel, { once: true });
    promise.then(
      (v) => {
        signal.removeEventListener("abort", cancel);
        resolve(v);
      },
      (e) => {
        signal.removeEventListener("abort", cancel);
        reject(e);
      },
    );
  });
}
