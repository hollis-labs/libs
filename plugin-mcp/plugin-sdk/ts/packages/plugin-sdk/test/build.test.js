import test from 'node:test';
import assert from 'node:assert/strict';
import { cp, chmod, mkdir, mkdtemp, readFile, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { collectFiles, decodeManifest, encodeManifest, treeDigest, verifyBundle, writeManifest } from '../dist/build/index.js';

const vector=fileURLToPath(new URL('../../../../manifest/testdata/build-v1/tree/',import.meta.url));
const manifest=decodeManifest(await readFile(join(vector,'plugin.yaml'),'utf8'));
async function scratch(t){const dir=await mkdtemp(join(tmpdir(),'sdk-build-'));t.after(()=>rm(dir,{recursive:true,force:true}));return dir;}

test('Node reproduces immutable Go tree digest and canonical manifest bytes',async(t)=>{
 const dir=await scratch(t);await cp(vector,dir,{recursive:true});
 const files=await collectFiles(dir);
 assert.equal(treeDigest(files),manifest.artifact.tree_sha256);
 assert.deepEqual(files,manifest.artifact.files);
 assert.equal(encodeManifest(manifest),await readFile(join(vector,'plugin.yaml'),'utf8'));
 assert.deepEqual(await verifyBundle(dir,manifest),manifest);
 await rm(join(dir,'plugin.yaml'));
 const {artifact,...declaration}=manifest;
 const written=await writeManifest(dir,declaration);
 assert.deepEqual(written.artifact,artifact);
 assert.equal(await readFile(join(dir,'plugin.yaml'),'utf8'),encodeManifest(manifest));
 await assert.rejects(writeManifest(dir,declaration),/EEXIST/u);
});
test('tree digest preserves exact bytes, record boundaries and executable mode',()=>{
 const reversed=[...manifest.artifact.files].reverse();assert.equal(treeDigest(reversed),manifest.artifact.tree_sha256);
 assert.notEqual(treeDigest(reversed.map(f=>({...f,executable:false}))),manifest.artifact.tree_sha256);
 for(const files of [[],[{path:'plugin.yaml',sha256:'0'.repeat(64)}],[{path:'bin/a',sha256:'0'.repeat(64)},{path:'BIN/A',sha256:'0'.repeat(64)}],[{path:'bin',sha256:'0'.repeat(64)},{path:'bin/a',sha256:'0'.repeat(64)}],[{path:'bin/../a',sha256:'0'.repeat(64)}],[{path:'bin/a',sha256:'bad'}],[{path:'bin/a\n',sha256:'0'.repeat(64)}],[{path:'bin/a',sha256:'0'.repeat(64)+'\n'}]])assert.throws(()=>treeDigest(files));
});
test('verify refuses tampering, extra/missing files, modes and reviewed manifest mismatch',async(t)=>{
 for(const kind of ['tamper','extra','missing','mode','manifest']){
  const dir=await scratch(t);await cp(vector,dir,{recursive:true});
  if(kind==='tamper')await writeFile(join(dir,'bin/server.js'),'changed');
  if(kind==='extra')await writeFile(join(dir,'extra'),'extra');
  if(kind==='missing')await rm(join(dir,'bin/server.js'));
  if(kind==='mode')await chmod(join(dir,'bin/server.js'),0o700);
  if(kind==='manifest')await writeFile(join(dir,'plugin.yaml'),encodeManifest({...manifest,name:'changed'}));
  await assert.rejects(verifyBundle(dir,manifest));
 }
});
test('collect and verify reject file, directory, manifest and root symlinks',async(t)=>{
 for(const kind of ['file','directory','manifest','root']){
  const dir=await scratch(t);const tree=join(dir,'tree');await cp(vector,tree,{recursive:true});
  let target=tree;
  if(kind==='file'){await rename(join(tree,'bin/server.js'),join(tree,'target'));await symlink('../target',join(tree,'bin/server.js'));}
  if(kind==='directory'){await rename(join(tree,'bin'),join(tree,'real'));await symlink('real',join(tree,'bin'));}
  if(kind==='manifest'){await rename(join(tree,'plugin.yaml'),join(tree,'real.yaml'));await symlink('real.yaml',join(tree,'plugin.yaml'));}
  if(kind==='root'){target=join(dir,'link');await symlink('tree',target);}
  await assert.rejects(verifyBundle(target),/symlink|real directory/u);
 }
});
test('strict manifest validation rejects unknown, null, malformed and ambiguous declarations',()=>{
 const raw=encodeManifest(manifest);
 for(const bad of [
  raw.replace('"protocol": 2','"protocol": 1'),raw.replace('"protocol": 2','"protocol": 2.0'),
  raw.replace('"runtime": "node"','"runtime": "node", "flags": []'),
  raw.replace('"once": false','"once": null'),raw.replace('"once": false','"once": "true"'),
  raw.replace('"view": "summary"','"view": " "'),
  raw.replace('"view": "summary"','"view": "summary\\n"'),
  raw.replace('"priority": 0','"priority": 0e0'),
  raw.replace('"min": "22.0.0"','"min": "22.0.0", "max": "21.0.0"'),
  raw.replace('"min": "22.0.0"','"Min": "22.0.0"'),
  raw.replace('"name": "Vector \\u003c\\u0026\\u003e"','"name": "a", "na\\u006de": "b"'),
  raw.replace('"required": true','"required": true, "value": "secret"'),
  raw+'{}', 'null', '['.repeat(66)+'0'+']'.repeat(66),
 ])assert.throws(()=>decodeManifest(bad));
 for(const bad of [NaN,Infinity,undefined,123n,()=>{},new Date(),2**60])assert.throws(()=>encodeManifest({...manifest,nanite:{bad}}));
 const cycle={};cycle.self=cycle;assert.throws(()=>encodeManifest({...manifest,nanite:cycle}));
});
test('build refuses a path with no payload or a legacy manifest declaration',async(t)=>{
 const dir=await scratch(t);await mkdir(join(dir,'bin'));
 const {artifact,...declaration}=manifest;
 assert.ok(artifact);
 await assert.rejects(writeManifest(dir,declaration),/nonempty/u);
 await writeFile(join(dir,'bin/server.js'),'worker');
 await assert.rejects(writeManifest(dir,{...declaration,entrypoint:{command:'bin/server.js'}}),/unknown field/u);
});
