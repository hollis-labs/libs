/** Node-only build helpers. Import this subpath in author build scripts, never
 * in a browser or plugin worker. No runtime dependencies or host policy. */
import { constants } from 'node:fs';
import { lstat, open } from 'node:fs/promises';
import { join } from 'node:path';
import { collectFiles, treeDigest } from './artifact.js';
import { decodeManifest, encodeManifest } from './manifest.js';
import type { BuildManifest, ManifestDeclaration } from './types.js';
export { collectFiles, treeDigest } from './artifact.js';
export { decodeManifest, encodeManifest, validateManifest } from './manifest.js';
export type { Artifact, ArtifactFile, BuildManifest, ConfigField, ConfigSecret, HookDeclaration, ManifestDeclaration, ToolDeclaration, VersionRange } from './types.js';

/** Write a fresh staged manifest. Refuses to overwrite any existing manifest.
 * Build payloads first, then call this, verify, and publish an immutable snapshot.
 * A failed build never authorizes replacing a host's reviewed installation. */
export async function writeManifest(directory:string,declaration:ManifestDeclaration):Promise<BuildManifest>{
  const files=await collectFiles(directory);
  const manifest:BuildManifest={...declaration,artifact:{files,tree_sha256:treeDigest(files)}};
  const encoded=encodeManifest(manifest);
  const file=await open(join(directory,'plugin.yaml'),constants.O_WRONLY|constants.O_CREAT|constants.O_EXCL,0o600);
  try{await file.writeFile(encoded);await file.sync();}finally{await file.close();}
  await verifyBundle(directory,manifest);
  return manifest;
}
/** Check exact payload bytes/modes and optionally the reviewed declaration.
 * Caller prevents concurrent mutation and launches this same verified snapshot.
 * This helper is not a sandbox, install approval or atomic filesystem lock. */
export async function verifyBundle(directory:string,reviewed?:BuildManifest):Promise<BuildManifest>{
  const files=await collectFiles(directory);
  const filename=join(directory,'plugin.yaml');const st=await lstat(filename);
  if(!st.isFile()||st.isSymbolicLink()||(st.mode&0o6000)!==0)throw new Error('manifest must be a regular file');
  const file=await open(filename,constants.O_RDONLY|(constants.O_NOFOLLOW??0));
  let manifest:BuildManifest;
  try {const stat=await file.stat();if(!stat.isFile()||stat.size>1<<20)throw new Error('invalid manifest file');manifest=decodeManifest(await file.readFile('utf8'));}finally{await file.close();}
  const sorted=(f:typeof files)=>[...f].sort((a,b)=>a.path<b.path?-1:a.path>b.path?1:0).map(x=>({path:x.path,sha256:x.sha256,executable:!!x.executable}));
  if(JSON.stringify(sorted(files))!==JSON.stringify(sorted(manifest.artifact.files))||treeDigest(files)!==manifest.artifact.tree_sha256)throw new Error('bundle payload differs from manifest inventory');
  if(reviewed&&encodeManifest(reviewed)!==encodeManifest(manifest))throw new Error('bundle manifest differs from reviewed declaration');
  return manifest;
}
