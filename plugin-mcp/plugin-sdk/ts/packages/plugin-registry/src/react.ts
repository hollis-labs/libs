/**
 * The React adapter — the default implementation for a host that renders
 * React components, which is what the reference implementation does.
 *
 * It is a separate entry point so the core stays dependency-free, mirroring
 * the Go module's own boundary. A host on React gets the reference behaviour
 * from `createReactPluginRegistry()`; a host that is not on React never loads
 * this file.
 */
import { lazy, useSyncExternalStore } from 'react'
import type { ComponentType, LazyExoticComponent } from 'react'
import { createPluginRegistry } from './loader.js'
import type {
  AdoptedContribution,
  PluginRegistry,
  PluginRegistryOptions,
  ResolvedContribution,
} from './loader.js'

/**
 * Plugin components have varied props, and this adapter is the boundary where
 * that is true — a host narrows at its own render site.
 */
type AnyComponent = ComponentType<Record<string, never>>

/**
 * Wrap a resolved export in `React.lazy`, so it matches the shape a host's
 * build-time registry already holds and renders under the Suspense boundary
 * that is already there.
 */
export function reactAdopt(resolved: ResolvedContribution): LazyExoticComponent<AnyComponent> {
  const component = resolved.export as AnyComponent
  return lazy(() => Promise.resolve({ default: component }))
}

/**
 * A registry pre-wired with `reactAdopt`. Every other option is still an
 * override, and a host that supplies its own `adopt` keeps it.
 */
export function createReactPluginRegistry(options: PluginRegistryOptions = {}): PluginRegistry {
  return createPluginRegistry({ ...options, adopt: options.adopt ?? reactAdopt })
}

/**
 * Re-render when the registry changes. `subscribe` and `version` are stable
 * per registry instance, which is what `useSyncExternalStore` requires.
 */
export function usePluginRegistryVersion(registry: PluginRegistry): number {
  return useSyncExternalStore(registry.subscribe, registry.version, registry.version)
}

/** The adopted contribution at (kind, key), re-read when the registry changes. */
export function usePluginContribution(
  registry: PluginRegistry,
  kind: string,
  key: string,
): AdoptedContribution | undefined {
  usePluginRegistryVersion(registry)
  return registry.get(kind, key)
}

/** Every resolved contribution of a kind, re-read when the registry changes. */
export function usePluginContributions(
  registry: PluginRegistry,
  kind: string,
): AdoptedContribution[] {
  usePluginRegistryVersion(registry)
  return registry.list(kind)
}
