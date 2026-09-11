/**
 * The plugin registry loader.
 *
 * Given a registry response from a host, it dynamic-imports each plugin's ES
 * module bundle, pulls the named exports the registry names, and keeps an
 * in-memory table a host can look contributions up in. Load failures are
 * isolated and attributable per plugin: one bad bundle is one plugin's
 * absence, not a blank surface.
 *
 * It works with no configuration at all. `createPluginRegistry()` imports
 * modules, injects stylesheets, resolves exports and notifies subscribers.
 * Every option is an override for a host that needs something else — most
 * usefully `adopt`, which is where a host that must contain plugin code puts
 * that containment.
 *
 * What it does not know, by construction: what a contribution kind means
 * (kinds are opaque strings and their metadata is opaque JSON), any host's
 * trust or isolation model, and where bundles are served from or under what
 * policy.
 */
import { PROTOCOL } from './types.js'
import type { PluginRegistryResponse, RegistryPlugin, RegistryRuntime } from './types.js'
import { defaultStylesheetSink } from './stylesheets.js'
import type { StylesheetSink } from './stylesheets.js'

/** A contribution as the host declared it, before any bundle was imported. */
export interface DeclaredContribution {
  kind: string
  key: string
  pluginId: string
  exportName: string
  meta?: unknown
}

/** A declared contribution whose export has been pulled from a live module. */
export interface ResolvedContribution extends DeclaredContribution {
  /** The value found on the module under `exportName`. */
  export: unknown
}

/** What `adopt` returned, indexed by (kind, key). */
export interface AdoptedContribution extends DeclaredContribution {
  /** With the default `adopt`, the raw export. */
  value: unknown
}

/** A contribution this registry declined to resolve. */
export interface Refusal {
  kind: string
  key: string
  /** Empty when the contribution was too malformed to name an owner. */
  pluginId: string
  reason: 'reserved' | 'invalid'
}

/** A plugin whose bundle did not import. */
export interface PluginLoadError {
  pluginId: string
  url: string
  reason: string
}

/**
 * Everything worth telling a host about, as data rather than console output.
 * A library that writes to the console is a library a host cannot quiet.
 */
export type LoaderDiagnostic =
  | { type: 'protocol-refused'; expected: number; received: unknown }
  | { type: 'bundle-loaded'; pluginId: string; url: string }
  | { type: 'bundle-load-failed'; pluginId: string; url: string; reason: string }
  | { type: 'plugin-unloaded'; pluginId: string }
  | { type: 'export-missing'; pluginId: string; kind: string; key: string; exportName: string }
  | {
      type: 'contribution-refused'
      pluginId: string
      kind: string
      key: string
      reason: 'reserved' | 'invalid'
    }

/** What one `sync` did. */
export interface SyncResult {
  /** False when the response was refused outright; state is then unchanged. */
  accepted: boolean
  /** Plugin ids whose module is currently resolved. */
  loaded: string[]
  /** Plugin ids whose bundle failed to import. */
  failed: string[]
  /** Contributions declared by the response and not refused. */
  declared: number
  /** Declared contributions whose export resolved. */
  resolved: number
  /** Contributions refused as reserved or malformed. */
  refused: number
}

export interface PluginRegistrySnapshot {
  protocol: number
  version: number
  plugins: Array<{
    id: string
    bundleUrl: string
    stylesheetUrl?: string
    runtime?: RegistryRuntime
    loaded: boolean
  }>
  contributions: Array<DeclaredContribution & { resolved: boolean }>
  errors: PluginLoadError[]
  refusals: Refusal[]
}

export interface PluginRegistryOptions {
  /**
   * How a bundle URL becomes a module. Defaults to a dynamic `import()`.
   * Injectable so tests need no network and no bundler.
   */
  importModule?: (url: string) => Promise<Record<string, unknown>>

  /**
   * Where a plugin's stylesheet goes. Defaults to a `<link>` in the ambient
   * document's head, or nothing where there is no document. Pass `false` for
   * a registry that touches no DOM at all.
   */
  stylesheets?: StylesheetSink | false

  /**
   * Turns a resolved export into whatever this host wants to hold.
   *
   * Defaults to the identity — the raw export. This is the override a host
   * reaches for when it needs to wrap, contain or adapt plugin code;
   * `@hollis-labs/plugin-registry/react` supplies one that wraps in
   * `React.lazy`.
   *
   * Called once per (kind, key) per module load. Its return value keeps
   * identity across syncs while the owning plugin, the export name and the
   * loaded module are unchanged, so a host holding a React component does not
   * remount on every reconcile.
   */
  adopt?: (resolved: ResolvedContribution) => unknown

  /**
   * Names this host's core owns. A contribution claiming one is refused,
   * recorded in `refusals()` and reported as a diagnostic. Defaults to
   * nothing reserved.
   *
   * This is the rule that a plugin may claim an unclaimed name and may never
   * displace a core one. A host may also enforce it structurally by consulting
   * its own registry first — but a structural refusal is silent, and a plugin
   * that tried to take a core name is worth being able to see.
   */
  reserved?: (kind: string, key: string) => boolean

  /** Structured diagnostics. Defaults to no output at all. */
  onDiagnostic?: (event: LoaderDiagnostic) => void
}

export interface PluginRegistry {
  /**
   * Reconcile against a registry response: import what is new, drop what is
   * gone, and rebuild the contribution table.
   *
   * Resolves once every bundle this response wants has settled. Safe to call
   * concurrently; a later call's view of what is wanted wins over an earlier
   * call's in-flight import.
   */
  sync(response: PluginRegistryResponse): Promise<SyncResult>

  /** The adopted contribution at (kind, key), if its export resolved. */
  get(kind: string, key: string): AdoptedContribution | undefined

  /** Every resolved contribution of a kind, in the order the host declared them. */
  list(kind: string): AdoptedContribution[]

  /**
   * The plugin that declared (kind, key), whether or not its bundle loaded —
   * so a host can attribute a contribution that is missing because its plugin
   * failed. Refused contributions are not reported here; they are in
   * `refusals()`, because attributing a refused claim to its would-be owner
   * invites a host to credit a core name to a plugin.
   */
  ownerOf(kind: string, key: string): string | undefined

  /** Plugins whose bundle failed to import, by plugin id. */
  errors(): readonly PluginLoadError[]

  /** Contributions refused as reserved or malformed. */
  refusals(): readonly Refusal[]

  /**
   * Drop one plugin's module and registrations. The next `sync` re-imports
   * it, which is what makes this a reload primitive rather than a removal.
   */
  unload(pluginId: string): void

  /** Drop everything, including the declared view. */
  clear(): void

  /** Subscribe to change. Stable reference, for `useSyncExternalStore`. */
  subscribe(listener: () => void): () => void

  /** A counter that advances on every change. Stable reference. */
  version(): number

  /** Everything this registry currently holds, for host devtools. */
  snapshot(): PluginRegistrySnapshot
}

/**
 * Joins the parts of a composite map key. A unit separator cannot appear in a
 * kind, a key, a plugin id or an export name, so the join is unambiguous.
 */
const SEP = '\u001f'

export function createPluginRegistry(options: PluginRegistryOptions = {}): PluginRegistry {
  const importModule = options.importModule ?? defaultImportModule
  const stylesheets =
    options.stylesheets === false ? null : (options.stylesheets ?? defaultStylesheetSink())
  const adopt = options.adopt ?? ((resolved: ResolvedContribution) => resolved.export)
  const reserved = options.reserved
  const emit = options.onDiagnostic ?? ((): void => {})

  // The last accepted response's declared view. Held so `unload` can rebuild
  // the table without needing a new response.
  let declared: DeclaredContribution[] = []
  let declaredPlugins = new Map<string, RegistryPlugin>()
  let refusals: Refusal[] = []

  /** pluginId -> the module currently loaded, and the URL it came from. */
  const modules = new Map<string, { url: string; module: Record<string, unknown>; epoch: number }>()
  let epochCounter = 0

  /**
   * The bundle URL we currently want loaded per plugin. Rewritten
   * synchronously at the start of every sync, before any await, so an
   * in-flight import can tell whether it is still wanted before committing.
   */
  const desired = new Map<string, string>()
  const inflight = new Map<string, Promise<void>>()
  const errors = new Map<string, PluginLoadError>()
  const owners = new Map<string, string>()

  /** kind -> key -> adopted contribution. Rebuilt whole on every sync. */
  const table = new Map<string, Map<string, AdoptedContribution>>()
  /**
   * Memoized `adopt` results, so rebuilding the table does not produce a new
   * adopted value for a contribution nothing about which changed.
   */
  const adoptedValues = new Map<string, { signature: string; value: unknown }>()
  /** Plugin ids whose stylesheet this registry has asked the sink to hold. */
  const styled = new Set<string>()

  const listeners = new Set<() => void>()
  let versionCounter = 0

  const notify = (): void => {
    versionCounter++
    for (const listener of [...listeners]) listener()
  }

  const subscribe = (listener: () => void): (() => void) => {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  }

  const version = (): number => versionCounter

  function countResolved(): number {
    let total = 0
    for (const byKey of table.values()) total += byKey.size
    return total
  }

  function result(accepted: boolean): SyncResult {
    return {
      accepted,
      loaded: [...modules.keys()].sort(),
      failed: [...errors.keys()].sort(),
      declared: declared.length,
      resolved: countResolved(),
      refused: refusals.length,
    }
  }

  function dropModule(pluginId: string): void {
    if (!modules.delete(pluginId)) return
    emit({ type: 'plugin-unloaded', pluginId })
  }

  function reconcileStylesheets(): void {
    if (!stylesheets) return
    const wanted = new Set<string>()
    for (const [pluginId, plugin] of declaredPlugins) {
      if (!plugin.stylesheet_url) continue
      wanted.add(pluginId)
      stylesheets.ensure(pluginId, plugin.stylesheet_url)
      styled.add(pluginId)
    }
    for (const pluginId of [...styled]) {
      if (wanted.has(pluginId)) continue
      stylesheets.remove(pluginId)
      styled.delete(pluginId)
    }
  }

  /**
   * Rebuild the contribution table from (declared view, loaded modules).
   *
   * Whole, every time, rather than incrementally. The reference implementation
   * wired contributions only when a plugin's bundle URL changed, and tracked
   * what each bundle had registered so it could unregister exactly those. That
   * made the contribution set a function of the BUNDLE rather than of the
   * manifest: a host whose manifest gained or lost a contribution without the
   * bundle bytes changing published a new registry response the browser never
   * applied. In a host-manifest-authoritative model that is the one path where
   * the manifest quietly is not authoritative.
   *
   * Deriving the table instead of maintaining it removes the class. The
   * bundle-unchanged optimisation survives where it belongs — on the IMPORT,
   * which is the expensive part — and the bookkeeping it needed does not.
   */
  function rebuild(): void {
    table.clear()
    const live = new Set<string>()

    for (const contribution of declared) {
      const loaded = modules.get(contribution.pluginId)
      if (!loaded) continue // declared, attributable, unresolved

      const exported = loaded.module[contribution.exportName]
      if (!isResolvableExport(exported)) {
        emit({
          type: 'export-missing',
          pluginId: contribution.pluginId,
          kind: contribution.kind,
          key: contribution.key,
          exportName: contribution.exportName,
        })
        continue
      }

      const id = indexKey(contribution.kind, contribution.key)
      const signature = [contribution.pluginId, contribution.exportName, loaded.epoch].join(SEP)
      let memo = adoptedValues.get(id)
      if (!memo || memo.signature !== signature) {
        memo = { signature, value: adopt({ ...contribution, export: exported }) }
        adoptedValues.set(id, memo)
      }
      live.add(id)

      let byKey = table.get(contribution.kind)
      if (!byKey) {
        byKey = new Map()
        table.set(contribution.kind, byKey)
      }
      // The entry is rebuilt so `meta` is always the host's latest; the
      // adopted VALUE is memoized so its identity survives.
      byKey.set(contribution.key, { ...contribution, value: memo.value })
    }

    for (const id of [...adoptedValues.keys()]) {
      if (!live.has(id)) adoptedValues.delete(id)
    }
  }

  function ensureModule(pluginId: string, url: string): Promise<void> {
    // Keyed on (pluginId, url) rather than plugin id alone, so a sync that
    // retargets a plugin to a new URL starts a fresh import instead of
    // adopting the in-flight promise for the old one.
    const key = indexKey(pluginId, url)
    const existing = inflight.get(key)
    if (existing) return existing

    const load = (async (): Promise<void> => {
      let module: Record<string, unknown>
      try {
        module = await importModule(url)
      } catch (err) {
        // Record the failure only if this load is still the wanted one. A
        // load superseded by a newer sync must not poison the newer bundle's
        // error state.
        if (desired.get(pluginId) === url) {
          const reason = err instanceof Error ? err.message : String(err)
          errors.set(pluginId, { pluginId, url, reason })
          emit({ type: 'bundle-load-failed', pluginId, url, reason })
        }
        return
      }
      // The registry may have moved on while we awaited. Imports resolve out
      // of order; the latest view of what is wanted wins.
      if (desired.get(pluginId) !== url) return
      epochCounter += 1
      modules.set(pluginId, { url, module, epoch: epochCounter })
      errors.delete(pluginId)
      emit({ type: 'bundle-loaded', pluginId, url })
    })()

    const tracked = load.finally(() => {
      if (inflight.get(key) === tracked) inflight.delete(key)
    })
    inflight.set(key, tracked)
    return tracked
  }

  async function sync(response: PluginRegistryResponse): Promise<SyncResult> {
    if (!response || response.protocol !== PROTOCOL) {
      emit({ type: 'protocol-refused', expected: PROTOCOL, received: response?.protocol })
      // Refuse the response whole and change nothing. A registry we cannot
      // read is not evidence that the host's plugins went away.
      return result(false)
    }

    // 1. Rebuild the declared view, synchronously, before any await.
    declaredPlugins = new Map(Object.entries(response.plugins ?? {}))
    declared = []
    refusals = []
    owners.clear()

    for (const [kind, byKey] of Object.entries(response.contributions ?? {})) {
      for (const [key, contribution] of Object.entries(byKey ?? {})) {
        const pluginId = typeof contribution?.plugin_id === 'string' ? contribution.plugin_id : ''
        const exportName = typeof contribution?.export === 'string' ? contribution.export : ''
        if (!kind || !key || !pluginId || !exportName) {
          refusals.push({ kind, key, pluginId, reason: 'invalid' })
          emit({ type: 'contribution-refused', pluginId, kind, key, reason: 'invalid' })
          continue
        }
        if (reserved?.(kind, key)) {
          refusals.push({ kind, key, pluginId, reason: 'reserved' })
          emit({ type: 'contribution-refused', pluginId, kind, key, reason: 'reserved' })
          continue
        }
        declared.push({ kind, key, pluginId, exportName, meta: contribution.meta })
        owners.set(indexKey(kind, key), pluginId)
      }
    }

    // 2. What we want loaded, also before any await — this is what an
    //    in-flight import checks itself against.
    desired.clear()
    for (const [pluginId, plugin] of declaredPlugins) {
      const url = effectiveBundleUrl(plugin)
      if (url) desired.set(pluginId, url)
    }

    // 3. Drop modules that are gone or retargeted, and errors that are stale.
    for (const [pluginId, loaded] of [...modules]) {
      if (desired.get(pluginId) !== loaded.url) dropModule(pluginId)
    }
    for (const pluginId of [...errors.keys()]) {
      if (!desired.has(pluginId)) errors.delete(pluginId)
    }

    // 4. Import what is missing. Per-plugin isolation: one bad bundle is one
    //    plugin's absence, not a blank surface.
    const loads: Array<Promise<void>> = []
    for (const [pluginId, url] of desired) {
      if (modules.get(pluginId)?.url === url) continue
      loads.push(ensureModule(pluginId, url))
    }
    await Promise.allSettled(loads)

    // 5. Derive the table, reconcile stylesheets, tell subscribers.
    rebuild()
    reconcileStylesheets()
    notify()
    return result(true)
  }

  return {
    sync,
    subscribe,
    version,

    get(kind, key) {
      return table.get(kind)?.get(key)
    },

    list(kind) {
      const byKey = table.get(kind)
      if (!byKey) return []
      const out: AdoptedContribution[] = []
      for (const contribution of declared) {
        if (contribution.kind !== kind) continue
        const entry = byKey.get(contribution.key)
        if (entry) out.push(entry)
      }
      return out
    },

    ownerOf(kind, key) {
      return owners.get(indexKey(kind, key))
    },

    errors() {
      return [...errors.values()].sort((a, b) => a.pluginId.localeCompare(b.pluginId))
    },

    refusals() {
      return [...refusals]
    },

    unload(pluginId) {
      dropModule(pluginId)
      errors.delete(pluginId)
      if (stylesheets && styled.delete(pluginId)) stylesheets.remove(pluginId)
      rebuild()
      notify()
    },

    clear() {
      for (const pluginId of [...styled]) {
        stylesheets?.remove(pluginId)
        styled.delete(pluginId)
      }
      modules.clear()
      inflight.clear()
      errors.clear()
      owners.clear()
      table.clear()
      adoptedValues.clear()
      desired.clear()
      declaredPlugins = new Map()
      declared = []
      refusals = []
      notify()
    },

    snapshot() {
      return {
        protocol: PROTOCOL,
        version: versionCounter,
        plugins: [...declaredPlugins].map(([id, plugin]) => ({
          id,
          bundleUrl: plugin.bundle_url ?? '',
          ...(plugin.stylesheet_url ? { stylesheetUrl: plugin.stylesheet_url } : {}),
          ...(plugin.runtime ? { runtime: plugin.runtime } : {}),
          loaded: modules.has(id),
        })),
        contributions: declared.map((contribution) => ({
          ...contribution,
          resolved: table.get(contribution.kind)?.has(contribution.key) ?? false,
        })),
        errors: [...errors.values()].sort((a, b) => a.pluginId.localeCompare(b.pluginId)),
        refusals: [...refusals],
      }
    },
  }
}

/**
 * Append the cache-bust token to the import URL.
 *
 * URL-keyed ES module caches never re-import the same URL, so without this a
 * reinstall at the same path is invisible to the browser. A plugin with no
 * token gets its raw URL and stays cached until the host supplies one.
 */
function effectiveBundleUrl(plugin: RegistryPlugin): string {
  const url = plugin.bundle_url ?? ''
  if (!url || !plugin.bundle_version) return url
  const separator = url.includes('?') ? '&' : '?'
  return `${url}${separator}v=${encodeURIComponent(plugin.bundle_version)}`
}

/**
 * Whether an export is worth handing to `adopt`.
 *
 * Deliberately permissive, and deliberately not a component check — this
 * package does not know what a host renders. React alone accepts more than
 * plain functions as element types (`memo` and `forwardRef` both return
 * objects), so anything callable or object-like passes and the host's own
 * renderer surfaces a bad export with better context than this could.
 */
function isResolvableExport(value: unknown): boolean {
  return typeof value === 'function' || (typeof value === 'object' && value !== null)
}

function indexKey(left: string, right: string): string {
  return `${left}${SEP}${right}`
}

function defaultImportModule(url: string): Promise<Record<string, unknown>> {
  return import(/* @vite-ignore */ url) as Promise<Record<string, unknown>>
}
