import { PROTOCOL, qualifiedKey } from "./types.js";
import type {
  PluginRegistryResponse,
  RegistryPlugin,
  RegistryContribution,
  Refusal,
} from "./types.js";
import { planResponse, validateResponse, own } from "./validation.js";
import type { AdmissionPolicy } from "./validation.js";
import { parseRegistryResponse, RegistryError } from "./parse.js";
import { checkRuntimes } from "./version.js";
import { defaultStylesheetSink } from "./stylesheets.js";
import type { StylesheetSink } from "./stylesheets.js";
export type { Refusal } from "./types.js";
export interface DeclaredContribution extends RegistryContribution {
  hostInstance: string;
  key: string;
  pluginId: string;
  exportName?: string;
}
export interface ResolvedContribution extends DeclaredContribution {
  export: unknown;
  isActive: () => boolean;
}
export interface AdoptedContribution extends DeclaredContribution {
  value: unknown;
  isActive: () => boolean;
}
export interface PluginLoadError {
  pluginId: string;
  generation: string;
  stage: "resolve" | "compat" | "load" | "dispose";
  reason: string;
}
export type LoaderDiagnostic =
  | { type: "response-refused"; reason: string }
  | { type: "contribution-refused"; refusal: Refusal }
  | { type: "plugin-failed"; error: PluginLoadError };
export interface SyncResult {
  accepted: boolean;
  loaded: string[];
  failed: string[];
  declared: number;
  resolved: number;
  refused: number;
}
export interface PluginRegistrySnapshot {
  protocol: number;
  hostInstance: string;
  revision: number;
  version: number;
  plugins: Array<{ id: string; generation: string; loaded: boolean }>;
  contributions: Array<DeclaredContribution & { resolved: boolean }>;
  errors: PluginLoadError[];
  refusals: Refusal[];
}
export interface VerifiedBundle {
  owner: string;
  generation: string;
  hostInstance: string;
  digest: string;
  bytes: Uint8Array;
  sourceUrl: string;
  signal: AbortSignal;
}
export interface PluginRegistryOptions extends AdmissionPolicy {
  runtimes?: Readonly<Record<string, string>>;
  allowPrerelease?: boolean;
  fetchBundle?: (url: string, signal: AbortSignal) => Promise<Uint8Array>;
  /** Called only after digest verification. Execute these bytes, not sourceUrl. */
  importModule?: (bundle: VerifiedBundle) => Promise<Record<string, unknown>>;
  stylesheets?: StylesheetSink | false;
  adopt?: (resolved: ResolvedContribution) => unknown;
  dispose?: (entry: AdoptedContribution) => void | Promise<void>;
  onDiagnostic?: (event: LoaderDiagnostic) => void;
}
export interface PluginRegistry {
  sync(response: PluginRegistryResponse | string): Promise<SyncResult>;
  get(kind: string, key: string): AdoptedContribution | undefined;
  list(kind: string): AdoptedContribution[];
  ownerOf(kind: string, key: string): string | undefined;
  errors(): readonly PluginLoadError[];
  refusals(): readonly Refusal[];
  /** Revoke immediately, then dispose; stale completions cannot restore it. */
  unload(owner: string, generation?: string): Promise<void>;
  clear(): Promise<void>;
  subscribe(listener: () => void): () => void;
  version(): number;
  isActive(owner: string, generation: string, epoch: string): boolean;
  snapshot(): PluginRegistrySnapshot;
}
/** Canonical JSON comparison preserves unchanged data identity across snapshots. */
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (value !== null && typeof value === "object")
    return `{${Object.keys(value)
      .sort()
      .map(
        (k) =>
          `${JSON.stringify(k)}:${canonical((value as Record<string, unknown>)[k])}`,
      )
      .join(",")}}`;
  return JSON.stringify(value) ?? "null";
}
function frozenJSON<T>(value: T): T {
  if (value !== null && typeof value === "object") {
    for (const child of Object.values(value)) frozenJSON(child);
    Object.freeze(value);
  }
  return value;
}
const identity = (kind: string, key: string) => JSON.stringify([kind, key]);
const signature = (p: RegistryPlugin) =>
  JSON.stringify([p.owner_generation, p.bundle_url, p.bundle_version]);
const declared = (
  c: RegistryContribution,
  epoch: string,
): DeclaredContribution => ({
  ...c,
  hostInstance: epoch,
  key: qualifiedKey(c.owner_id, c.local_key),
  pluginId: c.owner_id,
  ...(c.component ? { exportName: c.component.export } : {}),
});

export async function bundleDigest(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new Uint8Array(bytes).buffer,
  );
  return `sha256:${Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("")}`;
}
async function fetchBundle(
  url: string,
  signal: AbortSignal,
): Promise<Uint8Array> {
  const response = await fetch(url, { signal });
  if (!response.ok) throw new Error("Bundle fetch failed");
  return new Uint8Array(await response.arrayBuffer());
}
async function importVerified(
  bundle: VerifiedBundle,
): Promise<Record<string, unknown>> {
  // No second URL fetch. Self-contained bundles may use host importmap externals;
  // relative imports are deliberately unsupported by immutable data URLs.
  let binary = "";
  for (const byte of bundle.bytes) binary += String.fromCharCode(byte);
  const scope = encodeURIComponent(
    JSON.stringify([
      bundle.hostInstance,
      bundle.owner,
      bundle.generation,
      bundle.digest,
    ]),
  );
  return import(
    /* @vite-ignore */ `data:text/javascript;base64,${btoa(binary)}#${scope}`
  ) as Promise<Record<string, unknown>>;
}

export function createPluginRegistry(
  options: PluginRegistryOptions,
): PluginRegistry {
  const styles =
    options.stylesheets === false
      ? null
      : (options.stylesheets ?? defaultStylesheetSink());
  const emit = (event: LoaderDiagnostic) => {
    try {
      options.onDiagnostic?.(event);
    } catch {
      /* diagnostics cannot reopen authority */
    }
  };
  const listeners = new Set<() => void>();
  let versionCounter = 0,
    revision = 0,
    hostInstance = "",
    request = 0,
    wantedRevision = 0;
  let wantedHost = "";
  const revoked = new Set<string>();
  const generationKey = (epoch: string, owner: string, generation: string) =>
    JSON.stringify([epoch, owner, generation]);
  let current: PluginRegistryResponse | undefined;
  let entries = new Map<string, AdoptedContribution>();
  let declarations: DeclaredContribution[] = [];
  let refusals: Refusal[] = [],
    errors: PluginLoadError[] = [];
  const modules = new Map<
    string,
    { signature: string; module: Record<string, unknown> }
  >();
  const blocked = new Set<string>();
  const retiredHosts = new Set<string>();
  const pending = new Map<number, PluginRegistryResponse>();
  const styled = new Map<
    string,
    { owner: string; generation: string; url: string }
  >();
  let resourceCounter = 0;
  const resources = new WeakMap<
    AdoptedContribution,
    { order: number; disposed: boolean }
  >();
  const controllers = new Set<AbortController>();
  let tail = Promise.resolve();
  const styleKey = (owner: string, generation: string) =>
    JSON.stringify([hostInstance, owner, generation]);
  const notify = () => {
    versionCounter++;
    for (const listener of [...listeners]) {
      try {
        listener();
      } catch {
        /* notify other subscribers */
      }
    }
  };
  const result = (accepted: boolean): SyncResult => ({
    accepted,
    loaded: [...modules.keys()].sort(),
    failed: [...new Set(errors.map((e) => e.pluginId))].sort(),
    declared: declarations.length,
    resolved: entries.size,
    refused: refusals.length,
  });
  const fail = (
    pluginId: string,
    generation: string,
    stage: PluginLoadError["stage"],
    reason: string,
  ) => {
    const error = { pluginId, generation, stage, reason };
    errors.push(error);
    emit({ type: "plugin-failed", error });
  };
  async function disposeEntries(values: AdoptedContribution[]): Promise<void> {
    for (const entry of [...values].sort(
      (a, b) => (resources.get(b)?.order ?? 0) - (resources.get(a)?.order ?? 0),
    )) {
      const resource = resources.get(entry);
      if (resource?.disposed) continue;
      if (resource) resource.disposed = true;
      try {
        await options.dispose?.(entry);
      } catch {
        blocked.add(entry.owner_id);
        fail(
          entry.owner_id,
          entry.owner_generation,
          "dispose",
          "cleanup-incomplete",
        );
      }
    }
  }
  function revoke(owner: string, generation?: string): AdoptedContribution[] {
    const removed: AdoptedContribution[] = [];
    for (const [key, entry] of entries)
      if (
        entry.owner_id === owner &&
        (!generation || entry.owner_generation === generation)
      ) {
        entries.delete(key);
        removed.push(entry);
      }
    declarations = declarations.filter(
      (c) =>
        c.owner_id !== owner ||
        (!!generation && c.owner_generation !== generation),
    );
    refusals = refusals.filter(
      (f) =>
        f.owner_id !== owner ||
        (!!generation && f.owner_generation !== generation),
    );
    for (const candidate of pending.values()) {
      const candidatePlugin = own(candidate.plugins, owner);
      if (
        candidatePlugin &&
        (!generation || candidatePlugin.owner_generation === generation)
      )
        revoked.add(
          generationKey(
            candidate.host_instance,
            owner,
            candidatePlugin.owner_generation,
          ),
        );
    }
    if (generation && (hostInstance || wantedHost))
      revoked.add(generationKey(hostInstance || wantedHost, owner, generation));
    const plugin = current ? own(current.plugins, owner) : undefined;
    if (plugin && (!generation || plugin.owner_generation === generation)) {
      revoked.add(generationKey(hostInstance, owner, plugin.owner_generation));
      try {
        const key = styleKey(owner, plugin.owner_generation);
        styles?.remove(key);
        styled.delete(key);
      } catch {
        blocked.add(owner);
        fail(
          owner,
          plugin.owner_generation,
          "dispose",
          "stylesheet-cleanup-incomplete",
        );
      }
      modules.delete(owner);
      delete current!.plugins[owner];
      current!.refusals = current!.refusals.filter(
        (f) =>
          f.owner_id !== owner ||
          (!!generation && f.owner_generation !== generation),
      );
    }
    return removed;
  }
  function serialize<T>(fn: () => Promise<T>): Promise<T> {
    const operation = tail.then(fn);
    tail = operation.then(
      () => {},
      () => {},
    );
    return operation;
  }
  async function sync(
    response: PluginRegistryResponse | string,
  ): Promise<SyncResult> {
    let candidate: PluginRegistryResponse;
    try {
      if (typeof response === "string")
        response = parseRegistryResponse(response);
      const invalid = validateResponse(response);
      if (invalid) {
        emit({ type: "response-refused", reason: invalid });
        return result(false);
      }
      candidate = JSON.parse(
        JSON.stringify(response),
      ) as PluginRegistryResponse;
    } catch (error) {
      emit({
        type: "response-refused",
        reason:
          error instanceof RegistryError
            ? error.code
            : "ErrInvalidContribution",
      });
      return result(false);
    }
    if (
      retiredHosts.has(candidate.host_instance) ||
      (hostInstance && candidate.host_instance !== hostInstance) ||
      (wantedHost && candidate.host_instance !== wantedHost) ||
      candidate.revision < revision
    ) {
      emit({ type: "response-refused", reason: "stale-revision" });
      return result(false);
    }
    if (candidate.revision === revision) {
      if (current && canonical(candidate) === canonical(current))
        return result(true);
      emit({ type: "response-refused", reason: "revision-reused" });
      return result(false);
    }
    if (candidate.revision <= wantedRevision) {
      emit({ type: "response-refused", reason: "stale-pending-revision" });
      return result(false);
    }
    let plan;
    try {
      plan = planResponse(candidate, options);
    } catch {
      emit({ type: "response-refused", reason: "policy-failed" });
      return result(false);
    }
    for (const refusal of plan.refusals)
      emit({ type: "contribution-refused", refusal });
    if (plan.requiredFailed) {
      emit({ type: "response-refused", reason: "required-refused" });
      return result(false);
    }
    for (const [owner, p] of Object.entries(candidate.plugins)) {
      if (
        revoked.has(
          generationKey(candidate.host_instance, owner, p.owner_generation),
        )
      ) {
        emit({ type: "response-refused", reason: "generation-revoked" });
        return result(false);
      }
      if (blocked.has(owner)) {
        emit({ type: "response-refused", reason: "cleanup-incomplete" });
        return result(false);
      }
      const old = current ? own(current.plugins, owner) : undefined;
      if (
        old &&
        old.owner_generation === p.owner_generation &&
        signature(old) !== signature(p)
      ) {
        emit({ type: "response-refused", reason: "generation-reused" });
        return result(false);
      }
      if (
        !checkRuntimes(
          p.runtime ?? [],
          options.runtimes ?? {},
          options.allowPrerelease,
        )
      ) {
        emit({ type: "response-refused", reason: "runtime-incompatible" });
        return result(false);
      }
    }
    wantedRevision = candidate.revision;
    wantedHost = candidate.host_instance;
    const ticket = ++request,
      controller = new AbortController();
    controllers.add(controller);
    pending.set(ticket, candidate);
    const verified = new Map<string, VerifiedBundle>();
    try {
      for (const owner of new Set(
        plan.accepted
          .filter((c) => c.representation === "component")
          .map((c) => c.owner_id),
      )) {
        const p = candidate.plugins[owner];
        if (modules.get(owner)?.signature === signature(p)) continue;
        const bytes = new Uint8Array(
          await (options.fetchBundle ?? fetchBundle)(
            p.bundle_url!,
            controller.signal,
          ),
        );
        if ((await bundleDigest(bytes)) !== p.bundle_version)
          throw new Error("integrity-mismatch");
        verified.set(owner, {
          owner,
          generation: p.owner_generation,
          hostInstance: candidate.host_instance,
          digest: p.bundle_version!,
          bytes,
          sourceUrl: p.bundle_url!,
          signal: controller.signal,
        });
      }
    } catch {
      if (ticket === request) {
        wantedRevision = revision;
        wantedHost = hostInstance;
        emit({
          type: "response-refused",
          reason: "resolve-or-integrity-failed",
        });
      }
      pending.delete(ticket);
      controllers.delete(controller);
      return result(false);
    }

    return serialize(async () => {
      if (
        ticket !== request ||
        candidate.revision <= revision ||
        Object.entries(candidate.plugins).some(([owner, p]) =>
          revoked.has(
            generationKey(candidate.host_instance, owner, p.owner_generation),
          ),
        )
      )
        return result(false);
      errors = [];
      const changed = new Set<string>();
      for (const [owner, p] of Object.entries(current?.plugins ?? {}))
        if (
          !own(candidate.plugins, owner) ||
          signature(p) !== signature(candidate.plugins[owner])
        )
          changed.add(owner);
      const withdrawn: AdoptedContribution[] = [];
      for (const [id, entry] of entries) {
        const next = plan.accepted.find(
          (c) => identity(c.kind, qualifiedKey(c.owner_id, c.local_key)) === id,
        );
        if (
          !changed.has(entry.owner_id) &&
          (!next ||
            next.representation !== entry.representation ||
            next.component?.export !== entry.component?.export ||
            next.component?.region !== entry.component?.region ||
            canonical(next.declarative) !== canonical(entry.declarative) ||
            canonical(next.handler) !== canonical(entry.handler))
        ) {
          entries.delete(id);
          withdrawn.push(entry);
        }
      }
      for (const owner of changed) withdrawn.push(...revoke(owner));
      if (withdrawn.length || changed.size) notify(); // old entries fenced before any new import
      await disposeEntries(withdrawn);
      if (
        ticket !== request ||
        plan.accepted.some((c) => blocked.has(c.owner_id))
      )
        return result(false);
      const stage = new Map<string, AdoptedContribution>(),
        created: AdoptedContribution[] = [];
      const stagedModules = new Map(modules);
      try {
        for (const [owner, bundle] of verified) {
          const module = await (options.importModule ?? importVerified)(bundle);
          if (ticket !== request) return result(false);
          stagedModules.set(owner, {
            signature: signature(candidate.plugins[owner]),
            module,
          });
        }
        for (const c of plan.accepted) {
          const view = declared(c, candidate.host_instance),
            id = identity(c.kind, view.key),
            existing = entries.get(id);
          const resource = existing
            ? resources.get(existing)!
            : { order: ++resourceCounter, disposed: false };
          const isActive = () =>
            !resource.disposed &&
            hostInstance === candidate.host_instance &&
            own(current?.plugins ?? {}, c.owner_id)?.owner_generation ===
              c.owner_generation &&
            resources.get(entries.get(id)!) === resource &&
            !blocked.has(c.owner_id);
          let value: unknown;
          if (c.component) {
            const module = stagedModules.get(c.owner_id)?.module,
              exported = module ? own(module, c.component.export) : undefined;
            if (
              typeof exported !== "function" &&
              (typeof exported !== "object" || exported === null)
            ) {
              const refusal: Refusal = {
                owner_id: c.owner_id,
                owner_generation: c.owner_generation,
                kind: c.kind,
                local_key: c.local_key,
                required: c.required,
                reason: "export-missing",
              };
              plan.refusals.push(refusal);
              emit({ type: "contribution-refused", refusal });
              if (c.required) throw new Error("required-export-missing");
              continue;
            }
            value =
              existing?.owner_generation === c.owner_generation &&
              existing.exportName === c.component.export
                ? existing.value
                : (options.adopt ?? ((r) => r.export))({
                    ...view,
                    export: exported,
                    isActive,
                  });
          } else
            value =
              existing?.owner_generation === c.owner_generation
                ? existing.value
                : frozenJSON(
                    c.representation === "declarative"
                      ? c.declarative
                      : c.handler,
                  );
          const adopted = Object.freeze({
            ...view,
            component: frozenJSON(c.component),
            handler: frozenJSON(c.handler),
            declarative: frozenJSON(c.declarative),
            metadata: frozenJSON(c.metadata),
            value,
            isActive,
          });
          stage.set(id, adopted);
          const reused =
            existing && adopted.value === existing.value
              ? resources.get(existing)
              : undefined;
          resources.set(adopted, reused ?? resource);
          if (!reused) created.push(adopted);
        }
      } catch {
        await disposeEntries(created);
        for (const owner of verified.keys()) {
          revoked.add(
            generationKey(
              candidate.host_instance,
              owner,
              candidate.plugins[owner].owner_generation,
            ),
          );
          fail(
            owner,
            candidate.plugins[owner].owner_generation,
            "load",
            "activation-failed",
          );
        }
        notify();
        return result(false);
      }
      if (ticket !== request) {
        await disposeEntries(created);
        return result(false);
      }
      entries = stage;
      modules.clear();
      for (const [owner, module] of stagedModules) modules.set(owner, module);
      hostInstance = candidate.host_instance;
      revision = candidate.revision;
      current = candidate;
      declarations = plan.accepted.map((c) =>
        Object.freeze(declared(c, candidate.host_instance)),
      );
      refusals = plan.refusals;
      const wantedStyles = new Set<string>();
      for (const [owner, p] of Object.entries(candidate.plugins))
        if (modules.has(owner) && p.stylesheet_url) {
          const key = styleKey(owner, p.owner_generation);
          wantedStyles.add(key);
          try {
            styles?.ensure(key, p.stylesheet_url);
            styled.set(key, {
              owner,
              generation: p.owner_generation,
              url: p.stylesheet_url,
            });
          } catch {
            fail(owner, p.owner_generation, "load", "stylesheet-failed");
          }
        }
      for (const [key, style] of styled)
        if (!wantedStyles.has(key)) {
          try {
            styles?.remove(key);
            styled.delete(key);
          } catch {
            blocked.add(style.owner);
            fail(
              style.owner,
              style.generation,
              "dispose",
              "stylesheet-cleanup-incomplete",
            );
          }
        }
      notify();
      return result(true);
    }).finally(() => {
      controllers.delete(controller);
      pending.delete(ticket);
      if (ticket === request) {
        wantedRevision = revision;
        wantedHost = hostInstance;
      }
    });
  }
  async function unload(owner: string, generation?: string): Promise<void> {
    ++request;
    wantedRevision = revision;
    wantedHost = hostInstance;
    for (const controller of controllers) controller.abort();
    const removed = revoke(owner, generation);
    notify();
    await serialize(async () => {
      await disposeEntries(removed);
    });
  }
  return {
    sync,
    unload,
    async clear() {
      ++request;
      wantedRevision = revision;
      wantedHost = hostInstance;
      for (const controller of controllers) controller.abort();
      if (hostInstance || wantedHost)
        retiredHosts.add(hostInstance || wantedHost);
      for (const candidate of pending.values())
        retiredHosts.add(candidate.host_instance);
      const removed: AdoptedContribution[] = [];
      for (const owner of Object.keys(current?.plugins ?? {}))
        removed.push(...revoke(owner));
      entries.clear();
      declarations = [];
      modules.clear();
      refusals = [];
      current = undefined;
      revision = 0;
      hostInstance = "";
      wantedRevision = 0;
      wantedHost = "";
      notify();
      await serialize(async () => {
        await disposeEntries(removed);
      });
      // Cleanup failures remain quarantined; clearing state is not proof of cleanup.
    },
    get: (kind, key) => entries.get(identity(kind, key)),
    list: (kind) => [...entries.values()].filter((c) => c.kind === kind),
    ownerOf: (kind, key) =>
      declarations.find((c) => c.kind === kind && c.key === key)?.owner_id,
    errors: () => [...errors],
    refusals: () => [...refusals],
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    version: () => versionCounter,
    isActive: (owner, generation, epoch) =>
      hostInstance === epoch &&
      !!current &&
      own(current.plugins, owner)?.owner_generation === generation &&
      !blocked.has(owner),
    snapshot: () => ({
      protocol: PROTOCOL,
      hostInstance,
      revision,
      version: versionCounter,
      plugins: Object.entries(current?.plugins ?? {}).map(([id, p]) => ({
        id,
        generation: p.owner_generation,
        loaded: modules.has(id),
      })),
      contributions: declarations.map((c) => ({
        ...c,
        resolved: entries.has(identity(c.kind, c.key)),
      })),
      errors: [...errors],
      refusals: [...refusals],
    }),
  };
}
