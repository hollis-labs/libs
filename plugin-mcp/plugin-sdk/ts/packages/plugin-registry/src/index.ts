/**
 * `@hollis-labs/plugin-registry` — the browser half of the plugin contract.
 *
 * The Go half is `github.com/hollis-labs/plugin-sdk`, in the same repository.
 * `registry/registry.go` there is the other view of the wire contract this
 * package's `types.ts` declares.
 *
 * This entry point has no dependencies, mirroring the Go module's own
 * boundary. The React adapter — the default implementation for a host that
 * renders React components — is at `@hollis-labs/plugin-registry/react` and
 * takes React as an optional peer.
 */
export { PROTOCOL } from './types.js'
export type {
  PluginRegistryResponse,
  RegistryContribution,
  RegistryPlugin,
  RegistryRuntime,
} from './types.js'

export { createPluginRegistry } from './loader.js'
export type {
  AdoptedContribution,
  DeclaredContribution,
  LoaderDiagnostic,
  PluginLoadError,
  PluginRegistry,
  PluginRegistryOptions,
  PluginRegistrySnapshot,
  Refusal,
  ResolvedContribution,
  SyncResult,
} from './loader.js'

export {
  createLinkStylesheetSink,
  createNullStylesheetSink,
  defaultStylesheetSink,
} from './stylesheets.js'
export type { DocumentLike, LinkElementLike, StylesheetSink } from './stylesheets.js'
