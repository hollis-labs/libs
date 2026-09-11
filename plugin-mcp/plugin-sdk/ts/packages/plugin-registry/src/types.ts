/**
 * The registry wire contract — the TypeScript view.
 *
 * This is one of two views of a single definition. The Go view is
 * `libs/plugin-sdk/registry/registry.go` in the same repository, and the
 * field comments there are the contract's prose. Neither view generates the
 * other; both pin `PROTOCOL` and both round-trip their own types, so a host
 * and a loader at genuinely different versions disagree about the protocol
 * number rather than about a field.
 *
 * The model is host-manifest-authoritative: the host publishes what
 * registered and the browser resolves it. A plugin never declares its own
 * registrations at runtime, and this loader never learns a registration from
 * a bundle it imported.
 */

/**
 * The registry wire version. A host and a loader must agree on it exactly.
 * A loader that meets a protocol it does not know refuses the whole response
 * rather than guessing at fields — an unreadable registry is not evidence
 * that a host's plugins went away.
 *
 * Pinned by a test here and by `TestProtocolLockedAt1` on the Go side.
 */
export const PROTOCOL = 1

/** A shared runtime a bundle expects the host to provide. */
export interface RegistryRuntime {
  name: string
  version: string
}

/** What the browser needs in order to load one plugin's UI. */
export interface RegistryPlugin {
  /** The ES module to dynamic-import. Absent means no browser code. */
  bundle_url?: string
  /** An optional stylesheet loaded alongside the bundle. */
  stylesheet_url?: string
  /**
   * An opaque cache-bust token appended to the import URL.
   *
   * It is NOT an integrity hash and nothing verifies it — a host may
   * legitimately derive it from a modification time. The host's one
   * obligation is that it changes whenever the bytes at `bundle_url` change:
   * URL-keyed ES module caches never re-import the same URL, so a reinstall
   * at the same path with an unchanged token is invisible to the browser.
   */
  bundle_version?: string
  /**
   * Declares the shared runtime this bundle expects, e.g. React. Carried and
   * surfaced on `snapshot()`; this package does not enforce it. Recorded as a
   * declaration so a host that wants to refuse an incompatible bundle has the
   * information, and so the next reader does not mistake a carried field for
   * a check.
   */
  runtime?: RegistryRuntime
}

/** One named export a plugin offers under one host-defined kind. */
export interface RegistryContribution {
  /** The plugin that owns this contribution. Must appear in `plugins`. */
  plugin_id: string
  /**
   * The named export to pull from the plugin's module. Required and
   * explicit: a loader that infers an export name from an identifier is
   * guessing, and this loader refuses rather than guess.
   */
  export: string
  /**
   * Host-defined and opaque. Carries everything about a contribution only
   * the host understands — a kind version, a schema URL, a slot name, a
   * priority, a label, props. This package never reads it, which is what
   * stops one host's taxonomy reaching another's.
   */
  meta?: unknown
}

/** The whole registry document a host serves. */
export interface PluginRegistryResponse {
  protocol: number
  /** Plugin id -> what the browser needs to load it. */
  plugins: Record<string, RegistryPlugin>
  /**
   * Kind -> contribution key -> contribution. The kind is host-defined and
   * opaque here; the key is unique within its kind.
   *
   * One contribution per (kind, key). Where a host groups contributions — an
   * ordered toolbar, a priority-sorted rail — the group name and the ordering
   * belong in `meta`, because grouping and order are host taxonomy and no
   * loader reads them.
   */
  contributions: Record<string, Record<string, RegistryContribution>>
}
