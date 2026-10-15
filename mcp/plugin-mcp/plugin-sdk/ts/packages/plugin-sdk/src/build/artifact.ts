import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import { lstat, open, readdir } from 'node:fs/promises';
import { join, posix } from 'node:path';
import type { ArtifactFile } from './types.js';

export function validBundlePath(path:string):boolean {
  return /^[A-Za-z0-9._/-]+$(?![\s\S])/u.test(path)&&posix.normalize(path)===path&&!path.startsWith('/')&&path!=='.'&&path.split('/').every(p=>p!=='..'&&!p.endsWith('.'));
}
/** SHA-256(domain + sorted length/path/raw-hash/execute-byte records). */
export function treeDigest(files:readonly ArtifactFile[]):string {
  if(!files.length)throw new Error('artifact files must be nonempty');
  const seen=new Set<string>();
  for(const f of files){
    if(!validBundlePath(f.path)||f.path.toLowerCase()==='plugin.yaml'||seen.has(f.path.toLowerCase()))throw new Error('invalid/duplicate artifact path');
    if(!/^[0-9a-f]{64}$(?![\s\S])/u.test(f.sha256))throw new Error('invalid file SHA-256');
    if(f.executable!==undefined&&typeof f.executable!=='boolean')throw new Error('invalid executable bit');
    seen.add(f.path.toLowerCase());
  }
  for(const f of files){const parts=f.path.toLowerCase().split('/');for(let i=1;i<parts.length;i++)if(seen.has(parts.slice(0,i).join('/')))throw new Error('file/directory collision');}
  const h=createHash('sha256').update('plugin-sdk-artifact-v1\0');
  for(const f of [...files].sort((a,b)=>a.path<b.path?-1:a.path>b.path?1:0)){
    const name=Buffer.from(f.path);const size=Buffer.alloc(4);size.writeUInt32BE(name.length);
    h.update(size).update(name).update(Buffer.from(f.sha256,'hex')).update(Buffer.from([f.executable?1:0]));
  }
  return h.digest('hex');
}
/** Collect every regular payload file in a private immutable staged directory.
 * Symlinks (including directory links), special files and privileged modes fail.
 * plugin.yaml is checked separately and excluded from the payload tree digest. */
export async function collectFiles(directory:string):Promise<ArtifactFile[]> {
  const root=await lstat(directory);if(!root.isDirectory()||root.isSymbolicLink())throw new Error('bundle root must be a real directory');
  const files:ArtifactFile[]=[];
  async function walk(relative:string):Promise<void>{
    for(const name of (await readdir(join(directory,relative))).sort()){
      const path=relative?`${relative}/${name}`:name;
      const absolute=join(directory,path);const st=await lstat(absolute);
      if(st.isSymbolicLink())throw new Error(`bundle symlink rejected: ${path}`);
      if(st.isDirectory()){await walk(path);continue;}
      if(!st.isFile()||(st.mode&0o6000)!==0)throw new Error(`bundle special file/mode rejected: ${path}`);
      if(path==='plugin.yaml')continue;
      if(!validBundlePath(path))throw new Error(`invalid bundle path: ${path}`);
      const file=await open(absolute,constants.O_RDONLY|(constants.O_NOFOLLOW??0));
      try {
        const current=await file.stat();if(!current.isFile()||current.ino!==st.ino||current.dev!==st.dev)throw new Error('bundle changed during collection');
        const hash=createHash('sha256');
        for await (const chunk of file.createReadStream({autoClose:false}))hash.update(chunk);
        files.push({path,sha256:hash.digest('hex'),...((st.mode&0o111)!==0?{executable:true}:{})});
      } finally {await file.close();}
    }
  }
  await walk('');treeDigest(files);return files;
}
