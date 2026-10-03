/** Manifest-v2 authoring types, independent of the subprocess Init DTO. */
export interface VersionRange { min?: string; max?: string }
export interface ArtifactFile { path: string; sha256: string; executable?: boolean }
export interface Artifact { files: ArtifactFile[]; tree_sha256: string }
export interface ConfigField {
  type: 'string' | 'boolean' | 'integer' | 'number' | 'select';
  label?: string; description?: string; required?: boolean;
  default?: string; env?: string; options?: string[];
}
export interface ConfigSecret { label?: string; description?: string; required?: boolean; env?: string }
export interface ToolDeclaration {
  name: string; description: string; input_schema: Record<string, unknown>; effect: string;
  annotations?: { title?: string; readOnlyHint?: boolean; destructiveHint?: boolean; idempotentHint?: boolean; openWorldHint?: boolean };
}
export interface HookDeclaration {
  name: string; priority?: number; once?: boolean; view?: string;
  mode: 'sequential' | 'parallel' | 'bail' | 'waterfall' | 'async' | 'after_commit';
  timeout: number; on_error: 'open' | 'closed';
}
export interface ManifestDeclaration {
  schema_version: 2; id: string; name: string; description?: string; version: string;
  license?: string; homepage?: string; repository?: string; protocol: 2; runtime: 'subprocess';
  server: { runtime: 'node' | 'deno' | 'bun' | 'binary'; engines: Record<string, VersionRange>; entry: string };
  ui?: { bundle: string; stylesheet?: string; isolation: 'sandboxed-frame' | 'main-origin' };
  hooks?: HookDeclaration[];
  capabilities?: { name: string; reason?: string; optional?: boolean; metadata?: unknown }[];
  config?: { fields?: Record<string, ConfigField>; secrets?: Record<string, ConfigSecret> };
  tools?: ToolDeclaration[]; hosts: Record<string, VersionRange>;
  cerberus?: Record<string, unknown>; tangent?: Record<string, unknown>; nanite?: Record<string, unknown>;
}
export interface BuildManifest extends ManifestDeclaration { artifact: Artifact }
