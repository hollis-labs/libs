/** Registry v2 is independent of the plugin subprocess protocol. */
export const REGISTRY_VERSION = 2;
export const MAX_REVISION = Number.MAX_SAFE_INTEGER;
export type Representation = "declarative" | "component" | "handler";
export interface RegistryRuntime {
  name: string;
  min?: string;
  max?: string;
}
export interface RegistryPlugin {
  owner_generation: string;
  bundle_url?: string;
  /** sha256:<64 lowercase hex digits>, checked against executed bytes. */
  bundle_version?: string;
  stylesheet_url?: string;
  runtime?: RegistryRuntime[];
}
export interface KindDescriptor {
  schema_version: number;
  metadata_schema: unknown;
  representations: Representation[];
  regions: string[];
  required_capabilities: string[];
}
export interface RegionDescriptor {
  kinds: string[];
  representations: Representation[];
  context_schema: unknown;
  ordering: "priority-ascending" | "priority-descending" | "manifest";
}
export interface RegistryContribution {
  owner_id: string;
  owner_generation: string;
  local_key: string;
  kind: string;
  schema_version: number;
  required: boolean;
  /** Host projection. Unknown nonempty statuses project as unavailable and inactive. */
  status: string;
  status_reason?: string;
  representation: Representation;
  metadata: unknown;
  component?: { export: string; region: string };
  declarative?: unknown;
  /** Browser-safe reviewed public reference; never a credential/internal handle. */
  handler?: { id: string };
  public_binding?: string;
}
export interface Refusal {
  owner_id: string;
  owner_generation: string;
  kind: string;
  local_key: string;
  reason: string;
  required: boolean;
}
export interface PluginRegistryResponse {
  registry_version: number;
  host_instance: string;
  revision: number;
  plugins: Record<string, RegistryPlugin>;
  kinds: Record<string, KindDescriptor>;
  regions: Record<string, RegionDescriptor>;
  contributions: Record<string, Record<string, RegistryContribution>>;
  refusals: Refusal[];
}
export function qualifiedKey(owner: string, key: string): string {
  return `${owner}/${key}`;
}
